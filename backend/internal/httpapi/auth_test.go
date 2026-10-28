package httpapi_test

import (
	"testing"
	"time"
)

type account struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	IsTeacher bool   `json:"isTeacher"`
}

func TestCorrectCodeCreatesSession(t *testing.T) {
	h := newHarness(t)
	c := h.login("Anna@Example.com")
	st, b := c.do("GET", "/api/me", nil)
	if st != 200 {
		t.Fatalf("me: %d %s", st, b)
	}
	if me := decode[account](t, b); me.Email != "anna@example.com" || me.IsTeacher {
		t.Fatalf("unexpected account %+v", me)
	}
}

func TestWrongCodeDoesNotCreateSession(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	c.do("POST", "/api/auth/code", map[string]string{"email": "a@example.com"})
	wrong := "000000"
	if h.mail.last("a@example.com") == wrong {
		wrong = "111111"
	}
	if st, _ := c.do("POST", "/api/auth/verify", map[string]string{"email": "a@example.com", "code": wrong}); st != 401 {
		t.Fatalf("wrong code: want 401, got %d", st)
	}
	if st, _ := c.do("GET", "/api/me", nil); st != 401 {
		t.Fatalf("me without session: want 401, got %d", st)
	}
}

func TestExpiredCodeDoesNotCreateSession(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	c.do("POST", "/api/auth/code", map[string]string{"email": "a@example.com"})
	h.clock.Advance(codeTTL + time.Second)
	if st, _ := c.do("POST", "/api/auth/verify", map[string]string{"email": "a@example.com", "code": h.mail.last("a@example.com")}); st != 401 {
		t.Fatalf("expired code: want 401, got %d", st)
	}
}

func TestReusedCodeDoesNotCreateSession(t *testing.T) {
	h := newHarness(t)
	h.login("a@example.com")
	again := h.client()
	if st, _ := again.do("POST", "/api/auth/verify", map[string]string{"email": "a@example.com", "code": h.mail.last("a@example.com")}); st != 401 {
		t.Fatalf("reused code: want 401, got %d", st)
	}
}

func TestTooManyWrongCodesBurnTheCode(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	c.do("POST", "/api/auth/code", map[string]string{"email": "a@example.com"})
	code := h.mail.last("a@example.com")
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	for i := 0; i < 5; i++ {
		c.do("POST", "/api/auth/verify", map[string]string{"email": "a@example.com", "code": wrong})
	}
	if st, _ := c.do("POST", "/api/auth/verify", map[string]string{"email": "a@example.com", "code": code}); st != 401 {
		t.Fatalf("code after 5 failures: want 401, got %d", st)
	}
}

func TestLogoutEndsSession(t *testing.T) {
	h := newHarness(t)
	c := h.login("a@example.com")
	if st, _ := c.do("POST", "/api/auth/logout", nil); st != 204 {
		t.Fatalf("logout: %d", st)
	}
	if st, _ := c.do("GET", "/api/me", nil); st != 401 {
		t.Fatalf("me after logout: want 401, got %d", st)
	}
}

func TestSessionExpires(t *testing.T) {
	h := newHarness(t)
	c := h.login("a@example.com")
	h.clock.Advance(sessionTTL + time.Second)
	if st, _ := c.do("GET", "/api/me", nil); st != 401 {
		t.Fatalf("expired session: want 401, got %d", st)
	}
}

func TestAdminEmailIsTeacher(t *testing.T) {
	h := newHarness(t)
	c := h.login(adminEmail)
	_, b := c.do("GET", "/api/me", nil)
	if !decode[account](t, b).IsTeacher {
		t.Fatal("ADMIN_EMAIL account must be a teacher")
	}
	if st, b := c.do("GET", "/api/teacher/learners", nil); st != 200 {
		t.Fatalf("teacher route: %d %s", st, b)
	}
}

func TestLearnerIsRefusedOnTeacherRoutes(t *testing.T) {
	h := newHarness(t)
	c := h.login("a@example.com")
	if st, _ := c.do("GET", "/api/teacher/learners", nil); st != 403 {
		t.Fatalf("learner on teacher route: want 403, got %d", st)
	}
}

func TestGrantTeacherToExistingAccount(t *testing.T) {
	h := newHarness(t)
	h.login("b@example.com")
	if err := h.auth.EnsureTeacher(t.Context(), "b@example.com"); err != nil {
		t.Fatal(err)
	}
	c := h.login("b@example.com")
	if st, _ := c.do("GET", "/api/teacher/learners", nil); st != 200 {
		t.Fatalf("granted teacher: want 200, got %d", st)
	}
}

type course struct {
	ID        string `json:"id"`
	Direction string `json:"direction"`
	Status    string `json:"status"`
}

func TestLearnerCannotReadAnotherLearnersCourse(t *testing.T) {
	h := newHarness(t)
	a := h.login("a@example.com")
	st, b := a.do("POST", "/api/courses", map[string]string{"direction": "pm"})
	if st != 201 {
		t.Fatalf("create course: %d %s", st, b)
	}
	id := decode[course](t, b).ID
	if st, _ := a.do("GET", "/api/courses/"+id, nil); st != 200 {
		t.Fatalf("owner read: %d", st)
	}
	other := h.login("b@example.com")
	if st, _ := other.do("GET", "/api/courses/"+id, nil); st != 404 {
		t.Fatalf("other learner read: want 404, got %d", st)
	}
	_, b = other.do("GET", "/api/courses", nil)
	if list := decode[[]course](t, b); len(list) != 0 {
		t.Fatalf("other learner sees courses: %+v", list)
	}
}
