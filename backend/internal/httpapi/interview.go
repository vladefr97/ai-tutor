package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vladefr97/tutor-app/backend/internal/ops"
	"github.com/vladefr97/tutor-app/backend/internal/planning"
)

type message struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *server) listMessages(w http.ResponseWriter, r *http.Request) {
	c, ok := s.ownCourse(w, r)
	if !ok {
		return
	}
	rows, err := s.DB.Query(r.Context(), `select id, author, text, created_at from messages where course_id = $1 order by created_at, id`, c.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[message])
	if err != nil {
		internalError(w, r, err)
		return
	}
	if list == nil {
		list = []message{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) postMessage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.ownCourse(w, r)
	if !ok {
		return
	}
	var in struct{ Text string }
	if !readJSON(w, r, &in) {
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" || len([]rune(in.Text)) > 8000 {
		writeError(w, http.StatusBadRequest, "invalid_text", "Сообщение пустое или слишком длинное")
		return
	}
	op, err := s.Planning.LearnerMessage(r.Context(), c.ID, in.Text)
	if !opError(w, r, err) {
		writeJSON(w, http.StatusAccepted, op)
	}
}

func (s *server) skipInterview(w http.ResponseWriter, r *http.Request) {
	c, ok := s.ownCourse(w, r)
	if !ok {
		return
	}
	if opError(w, r, s.Planning.SkipInterview(r.Context(), c.ID)) {
		return
	}
	if c, ok = s.ownCourse(w, r); ok {
		writeJSON(w, http.StatusOK, c)
	}
}

func (s *server) getOperation(w http.ResponseWriter, r *http.Request) {
	op, err := s.Ops.Get(r.Context(), r.PathValue("operationId"), accountFrom(r.Context()).ID)
	if !opError(w, r, err) {
		writeJSON(w, http.StatusOK, op)
	}
}

func (s *server) retryOperation(w http.ResponseWriter, r *http.Request) {
	op, err := s.Ops.Retry(r.Context(), r.PathValue("operationId"), accountFrom(r.Context()).ID)
	if !opError(w, r, err) {
		writeJSON(w, http.StatusAccepted, op)
	}
}

// opError writes the response for domain errors and reports whether it did.
func opError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ops.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Операция не найдена")
	case errors.Is(err, ops.ErrBusy):
		writeError(w, http.StatusConflict, "busy", "Наставник ещё отвечает")
	case errors.Is(err, ops.ErrNotRetry):
		writeError(w, http.StatusConflict, "not_failed", "Повторить можно только операцию с ошибкой")
	case errors.Is(err, planning.ErrWrongState):
		writeError(w, http.StatusConflict, "wrong_state", "Сейчас это действие недоступно")
	default:
		internalError(w, r, err)
	}
	return true
}
