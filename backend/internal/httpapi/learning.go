package httpapi

import (
	"errors"
	"net/http"

	"github.com/vladefr97/tutor-app/backend/internal/learning"
)

func (s *server) postAttempt(w http.ResponseWriter, r *http.Request) {
	c, ok := s.ownCourse(w, r)
	if !ok {
		return
	}
	var in struct {
		ItemID string          `json:"itemId"`
		Answer learning.Answer `json:"answer"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	at, err := s.Learning.Submit(r.Context(), c.ID, in.ItemID, in.Answer)
	if !learningError(w, r, err) {
		writeJSON(w, http.StatusCreated, at)
	}
}

func (s *server) listAttempts(w http.ResponseWriter, r *http.Request) {
	c, ok := s.ownCourse(w, r)
	if !ok {
		return
	}
	list, err := s.Learning.List(r.Context(), c.ID)
	if !learningError(w, r, err) {
		writeJSON(w, http.StatusOK, list)
	}
}

func (s *server) getAttempt(w http.ResponseWriter, r *http.Request) {
	at, err := s.Learning.Get(r.Context(), r.PathValue("attemptId"), accountFrom(r.Context()).ID)
	if !learningError(w, r, err) {
		writeJSON(w, http.StatusOK, at)
	}
}

func learningError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, learning.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_answer", "Ответ не подходит к этому вопросу")
	case errors.Is(err, learning.ErrPending):
		writeError(w, http.StatusConflict, "pending", "Предыдущий ответ ещё проверяется")
	case errors.Is(err, learning.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Попытка не найдена")
	case errors.Is(err, learning.ErrNotReady):
		writeError(w, http.StatusConflict, "not_ready", "Курс ещё не готов")
	default:
		internalError(w, r, err)
	}
	return true
}
