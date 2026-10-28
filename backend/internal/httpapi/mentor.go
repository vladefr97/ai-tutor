package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/vladefr97/tutor-app/backend/internal/mentor"
)

func (s *server) listChat(w http.ResponseWriter, r *http.Request) {
	c, ok := s.ownCourse(w, r)
	if !ok {
		return
	}
	list, err := s.Mentor.History(r.Context(), c.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) postChat(w http.ResponseWriter, r *http.Request) {
	c, ok := s.ownCourse(w, r)
	if !ok {
		return
	}
	var in struct{ Text, PageID string }
	if !readJSON(w, r, &in) {
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" || len([]rune(in.Text)) > 8000 || len(in.PageID) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_text", "Сообщение пустое или слишком длинное")
		return
	}
	op, err := s.Mentor.Post(r.Context(), c.ID, in.Text, in.PageID)
	if errors.Is(err, mentor.ErrNotReady) {
		writeError(w, http.StatusConflict, "not_ready", "Курс ещё не готов")
		return
	}
	if !opError(w, r, err) {
		writeJSON(w, http.StatusAccepted, op)
	}
}
