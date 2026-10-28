package httpapi_test

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vladefr97/tutor-app/backend/internal/agents"
)

func caseText(s string) map[string]any { return map[string]any{"text": s} }

func TestCaseIsReviewedAloneWithItsCriteriaAndGetsNoHints(t *testing.T) {
	h, g, a, course := readyPM(t)
	g.mu.Lock()
	g.reviewWork = func(req agents.TurnRequest) (agents.TurnResult, error) {
		var in struct{ Answer string }
		json.Unmarshal([]byte(req.Input), &in)
		if strings.Contains(in.Answer, "риск") {
			return workReview(true, "Все критерии выполнены."), nil
		}
		return workReview(false, "Не хватает: риски и меры реагирования."), nil
	}
	g.mu.Unlock()
	a.answer(course, "q-goal-single", opts("a")) // other work must not leak into the case review
	before := len(h.agent.all())

	st, at := a.answer(course, "t-plan-case", caseText("Цель: переехать за 6 недель. Задачи: ..."))
	if st != 201 || at.Status != "pending" {
		t.Fatalf("case attempt: %d %+v", st, at)
	}
	got := a.waitAttempt(at.ID)
	if got.Status != "failed" || got.Feedback == nil || !strings.Contains(*got.Feedback, "риски") || got.Hint != nil || got.Explanation != nil {
		t.Fatalf("failed case: %+v", got)
	}
	_, at = a.answer(course, "t-plan-case", caseText("Цель, задачи и три риска с мерами реагирования."))
	if got = a.waitAttempt(at.ID); got.Status != "passed" || got.Hint != nil || got.Explanation != nil {
		t.Fatalf("passed case: %+v", got)
	}

	reviews := h.agent.all()[before:]
	if len(reviews) != 2 || reviews[0].SessionDir == reviews[1].SessionDir || reviews[0].ThreadID != "" {
		t.Fatalf("each case attempt needs its own reviewer request: %+v", reviews)
	}
	var in map[string]any
	json.Unmarshal([]byte(reviews[0].Input), &in)
	task, _ := in["task"].(string)
	if in["mode"] != "case" || in["exam"] != false || !strings.Contains(task, "переезжает в новый офис") ||
		!strings.Contains(reviews[0].Input, "Названы три риска") || in["answer"] != "Цель: переехать за 6 недель. Задачи: ..." {
		t.Fatalf("reviewer input: %s", reviews[0].Input)
	}
	for _, leak := range []string{"q-goal-single", "Ищите формулировку", "К 1 марта", "exam-a-case"} {
		if strings.Contains(reviews[0].Input, leak) {
			t.Fatalf("reviewer input mixes in other work (%s): %s", leak, reviews[0].Input)
		}
	}
	_, b := a.do("GET", "/api/courses/"+course+"/attempts", nil)
	cases := 0
	for _, x := range decode[[]attempt](t, b) {
		if x.ItemID == "t-plan-case" {
			cases++
		}
	}
	if cases != 2 {
		t.Fatalf("case history: %s", b)
	}
}

func TestCaseReviewerErrorAndLateResult(t *testing.T) {
	h, g, a, course := readyPM(t)
	h.learn.ReviewTimeout = 200 * time.Millisecond
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	g.mu.Lock()
	g.reviewWork = func(req agents.TurnRequest) (agents.TurnResult, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			<-release
			return workReview(true, "Поздний зачёт"), nil
		}
		return workReview(false, "Замечания"), nil
	}
	g.mu.Unlock()
	_, first := a.answer(course, "t-plan-case", caseText("первый"))
	if got := a.waitAttempt(first.ID); got.Status != "error" {
		t.Fatalf("timed out review must be an error: %+v", got)
	}
	_, second := a.answer(course, "t-plan-case", caseText("второй"))
	if got := a.waitAttempt(second.ID); got.Status != "failed" {
		t.Fatalf("second: %+v", got)
	}
	close(release)
	h.learn.Wait()
	_, b := a.do("GET", "/api/attempts/"+first.ID, nil)
	_, c := a.do("GET", "/api/attempts/"+second.ID, nil)
	if decode[attempt](t, b).Status != "error" || decode[attempt](t, c).Status != "failed" {
		t.Fatalf("late pass changed attempts: %s %s", b, c)
	}
}

func TestCaseAttemptsArePrivateAndValidated(t *testing.T) {
	h, _, a, course := readyPM(t)
	if st, _ := h.login("b@example.com").answer(course, "t-plan-case", caseText("чужой")); st != 404 {
		t.Fatalf("other learner: %d", st)
	}
	for _, ans := range []map[string]any{caseText(" "), caseText(strings.Repeat("я", 20001)), opts("a")} {
		if st, _ := a.answer(course, "t-plan-case", ans); st != 400 {
			t.Fatalf("invalid case answer accepted: %d", st)
		}
	}
	if st, _ := a.answer(course, "exam-a-case", caseText("экзамен не через практику")); st != 400 {
		t.Fatalf("exam case via practice API: %d", st)
	}
}
