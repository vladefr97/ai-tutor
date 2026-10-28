// Package httpapi exposes the JSON API described in contracts/openapi.yaml
// and serves the built React app.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vladefr97/tutor-app/backend/internal/auth"
	"github.com/vladefr97/tutor-app/backend/internal/generation"
	"github.com/vladefr97/tutor-app/backend/internal/learning"
	"github.com/vladefr97/tutor-app/backend/internal/mentor"
	"github.com/vladefr97/tutor-app/backend/internal/ops"
	"github.com/vladefr97/tutor-app/backend/internal/planning"
	"github.com/vladefr97/tutor-app/backend/internal/visits"
)

const sessionCookie = "tutor_session"

type Deps struct {
	DB           *pgxpool.Pool
	Auth         *auth.Service
	Ops          *ops.Runner
	Planning     *planning.Service
	Generation   *generation.Service
	Learning     *learning.Service
	Mentor       *mentor.Service
	CookieSecure bool
	WebDir       string // built React app; empty disables static serving
}

type server struct{ Deps }

type ctxKey struct{}

func accountFrom(ctx context.Context) auth.Account { return ctx.Value(ctxKey{}).(auth.Account) }

func New(d Deps) http.Handler {
	s := &server{d}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/code", s.requestCode)
	mux.HandleFunc("POST /api/auth/verify", s.verify)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	mux.Handle("GET /api/me", s.learner(s.me))
	mux.Handle("GET /api/directions", s.learner(s.directions))
	mux.Handle("GET /api/courses", s.learner(s.listCourses))
	mux.Handle("POST /api/courses", s.learner(s.createCourse))
	mux.Handle("GET /api/courses/{courseId}", s.learner(s.getCourse))
	mux.Handle("GET /api/courses/{courseId}/messages", s.learner(s.listMessages))
	mux.Handle("POST /api/courses/{courseId}/messages", s.learner(s.postMessage))
	mux.Handle("POST /api/courses/{courseId}/interview/skip", s.learner(s.skipInterview))
	mux.Handle("GET /api/courses/{courseId}/plan", s.learner(s.getPlan))
	mux.Handle("POST /api/courses/{courseId}/plan/confirm", s.learner(s.confirmPlan))
	mux.Handle("GET /api/courses/{courseId}/content", s.learner(s.getContent))
	mux.Handle("POST /api/courses/{courseId}/attempts", s.learner(s.postAttempt))
	mux.Handle("GET /api/courses/{courseId}/attempts", s.learner(s.listAttempts))
	mux.Handle("GET /api/attempts/{attemptId}", s.learner(s.getAttempt))
	mux.Handle("GET /api/courses/{courseId}/chat", s.learner(s.listChat))
	mux.Handle("POST /api/courses/{courseId}/chat", s.learner(s.postChat))
	mux.Handle("GET /api/operations/{operationId}", s.learner(s.getOperation))
	mux.Handle("POST /api/operations/{operationId}/retry", s.learner(s.retryOperation))
	mux.Handle("GET /api/teacher/learners", s.teacher(s.listLearners))
	mux.Handle("GET /api/teacher/attendance", s.teacher(func(w http.ResponseWriter, r *http.Request) {
		a, err := visits.Count(r.Context(), s.DB, s.Auth.Now())
		if err != nil {
			internalError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, a)
	}))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "Маршрут не найден")
	})
	if d.WebDir != "" {
		mux.Handle("/", spa(d.WebDir))
	}
	return mux
}

// learner requires any valid session; the account comes only from the session.
func (s *server) learner(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Нужно войти")
			return
		}
		acc, err := s.Auth.Authenticate(r.Context(), c.Value)
		if errors.Is(err, auth.ErrNoSession) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Сессия завершена")
			return
		}
		if err != nil {
			internalError(w, r, err)
			return
		}
		if !acc.IsTeacher {
			// A lost visit must not fail the learner's request; it is logged instead.
			if err := visits.Record(r.Context(), s.DB, acc.ID, s.Auth.Now()); err != nil {
				slog.ErrorContext(r.Context(), "record visit", "err", err)
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, acc)))
	})
}

func (s *server) teacher(next http.HandlerFunc) http.Handler {
	return s.learner(func(w http.ResponseWriter, r *http.Request) {
		if !accountFrom(r.Context()).IsTeacher {
			writeError(w, http.StatusForbidden, "forbidden", "Доступно только преподавателю")
			return
		}
		next(w, r)
	})
}

func (s *server) requestCode(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email string }
	if !readJSON(w, r, &in) {
		return
	}
	if _, err := mail.ParseAddress(in.Email); err != nil || len(in.Email) > 254 {
		writeError(w, http.StatusBadRequest, "invalid_email", "Проверьте адрес почты")
		return
	}
	if err := s.Auth.RequestCode(r.Context(), in.Email); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) verify(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Code string }
	if !readJSON(w, r, &in) {
		return
	}
	token, acc, err := s.Auth.Verify(r.Context(), in.Email, in.Code)
	if errors.Is(err, auth.ErrInvalidCode) {
		writeError(w, http.StatusUnauthorized, "invalid_code", "Неверный или истёкший код")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: s.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: int(s.Auth.SessionTTL.Seconds())})
	writeJSON(w, http.StatusOK, acc)
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Нужно войти")
		return
	}
	if err := s.Auth.Logout(r.Context(), c.Value); err != nil {
		internalError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true})
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, accountFrom(r.Context()))
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Некорректный запрос")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

func internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "Внутренняя ошибка")
}

// spa serves files from dir and falls back to index.html for client routes.
func spa(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(filepath.Join(dir, filepath.Clean("/"+r.URL.Path))); err != nil {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})
}
