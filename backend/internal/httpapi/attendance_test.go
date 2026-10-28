package httpapi_test

import (
	"testing"
	"time"
)

type attendance struct {
	TotalLearners   int    `json:"totalLearners"`
	RegularlyActive int    `json:"regularlyActive"`
	Timezone        string `json:"timezone"`
}

func (h *harness) attendance(teacher *client) attendance {
	h.t.Helper()
	st, b := teacher.do("GET", "/api/teacher/attendance", nil)
	if st != 200 {
		h.t.Fatalf("attendance: %d %s", st, b)
	}
	return decode[attendance](h.t, b)
}

func TestRepeatedVisitsCountOneLearner(t *testing.T) {
	h := newHarness(t)
	teacher := h.login(adminEmail)
	teacher.do("GET", "/api/me", nil)
	a := h.login("a@example.com")
	a.do("GET", "/api/me", nil)
	a.do("GET", "/api/me", nil) // page refresh
	var rows int
	if err := h.db.QueryRow(t.Context(), `select count(*) from visits v join accounts a on a.id = v.account_id
		where a.email = 'a@example.com'`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("want 2 visit rows after refresh, got %d (%v)", rows, err)
	}
	if got := h.attendance(teacher); got.TotalLearners != 1 || got.RegularlyActive != 0 || got.Timezone != "Europe/Moscow" {
		t.Fatalf("after refreshes on one day: %+v", got)
	}
	h.clock.Advance(13 * time.Hour) // 15:00 → 04:00 next Moscow day, session still alive
	a.do("GET", "/api/me", nil)
	if got := h.attendance(teacher); got.TotalLearners != 1 || got.RegularlyActive != 1 {
		t.Fatalf("after a visit next day: %+v", got)
	}
}

func TestTeacherVisitsAreNotCounted(t *testing.T) {
	h := newHarness(t)
	teacher := h.login(adminEmail)
	teacher.do("GET", "/api/me", nil)
	h.clock.Advance(13 * time.Hour) // 15:00 → 04:00 next Moscow day, session still alive
	teacher.do("GET", "/api/me", nil)
	if got := h.attendance(teacher); got.TotalLearners != 0 || got.RegularlyActive != 0 {
		t.Fatalf("teacher counted: %+v", got)
	}
}

func TestLearnerIsRefusedAttendance(t *testing.T) {
	h := newHarness(t)
	a := h.login("a@example.com")
	if st, _ := a.do("GET", "/api/teacher/attendance", nil); st != 403 {
		t.Fatalf("learner on attendance: want 403, got %d", st)
	}
}
