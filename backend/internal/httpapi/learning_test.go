package httpapi_test

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vladefr97/tutor-app/backend/internal/agents"
)

type attempt struct {
	ID          string  `json:"id"`
	ItemID      string  `json:"itemId"`
	Status      string  `json:"status"`
	Feedback    *string `json:"feedback"`
	Hint        *string `json:"hint"`
	Explanation *string `json:"explanation"`
}

func workReview(passed bool, feedback string) agents.TurnResult {
	verdict := "changes_requested"
	if passed {
		verdict = "passed"
	}
	b, _ := json.Marshal(map[string]any{"kind": "work_review", "verdict": verdict, "feedback": feedback,
		"criteria": []any{map[string]any{"criterion": "Меры реагирования", "met": passed, "comment": ""}}})
	return agents.TurnResult{ThreadID: "work-review", Text: string(b)}
}

// readyPM returns a learner client and a ready PM course built from the pm-basics example.
func readyPM(t *testing.T) (*harness, *genScript, *client, string) {
	h, g := newGenHarness(t)
	a := h.login("a@example.com")
	c, op := a.confirmPM()
	if op.Status != "succeeded" || c.Status != "ready" {
		t.Fatalf("course not ready: %+v", c)
	}
	return h, g, a, c.ID
}

func (c *client) answer(courseID, itemID string, answer map[string]any) (int, attempt) {
	c.h.t.Helper()
	st, b := c.do("POST", "/api/courses/"+courseID+"/attempts", map[string]any{"itemId": itemID, "answer": answer})
	if st != 201 {
		return st, attempt{}
	}
	return st, decode[attempt](c.h.t, b)
}

func (c *client) waitAttempt(id string) attempt {
	c.h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, b := c.do("GET", "/api/attempts/"+id, nil)
		if at := decode[attempt](c.h.t, b); at.Status != "pending" {
			return at
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.h.t.Fatalf("attempt %s is still pending", id)
	return attempt{}
}

func opts(ids ...string) map[string]any { return map[string]any{"options": ids} }

func TestMultipleChoiceNeedsTheExactSetInAnyOrder(t *testing.T) {
	_, _, a, course := readyPM(t)
	for _, tc := range []struct {
		options []string
		want    string
	}{{[]string{"a"}, "failed"}, {[]string{"a", "b", "c"}, "failed"}, {[]string{"b", "a"}, "passed"}} {
		st, at := a.answer(course, "q-risks-multi", opts(tc.options...))
		if st != 201 || at.Status != tc.want {
			t.Fatalf("%v: %d %+v", tc.options, st, at)
		}
	}
}

func TestFirstMistakeGivesAHintTheSecondAnExplanation(t *testing.T) {
	_, _, a, course := readyPM(t)
	_, first := a.answer(course, "q-goal-single", opts("a"))
	if first.Status != "failed" || first.Hint == nil || !strings.Contains(*first.Hint, "проверить") || first.Explanation != nil {
		t.Fatalf("first mistake: %+v", first)
	}
	_, second := a.answer(course, "q-goal-single", opts("c"))
	if second.Status != "failed" || second.Explanation == nil || !strings.Contains(*second.Explanation, "К 1 марта") {
		t.Fatalf("second mistake: %+v", second)
	}
	// Going on is not blocked: the learner can still answer and pass.
	if _, third := a.answer(course, "q-goal-single", opts("b")); third.Status != "passed" || third.Hint != nil {
		t.Fatalf("after explanation: %+v", third)
	}
}

func TestInvalidAnswersAreRejected(t *testing.T) {
	_, _, a, course := readyPM(t)
	for _, tc := range []struct {
		item   string
		answer map[string]any
	}{
		{"q-goal-single", opts("a", "b")},              // single choice with two options
		{"q-goal-single", opts("z")},                   // unknown option
		{"q-risks-short", map[string]any{"text": " "}}, // empty text
		{"b-goal-intro", map[string]any{"text": "x"}},  // not a question
		{"nope", opts("a")},
	} {
		if st, _ := a.answer(course, tc.item, tc.answer); st != 400 {
			t.Fatalf("%s %v: want 400, got %d", tc.item, tc.answer, st)
		}
	}
}

func TestShortAnswerIsJudgedByTheWorkReviewerInAFreshRequest(t *testing.T) {
	h, g, a, course := readyPM(t)
	g.mu.Lock()
	g.reviewWork = func(req agents.TurnRequest) (agents.TurnResult, error) {
		var in struct{ Answer string }
		json.Unmarshal([]byte(req.Input), &in)
		if strings.Contains(in.Answer, "заранее") {
			return workReview(true, "Верно: меры готовят заранее."), nil
		}
		return workReview(false, "Не хватает: зачем готовить меры заранее."), nil
	}
	g.mu.Unlock()
	before := len(h.agent.all())
	_, at := a.answer(course, "q-risks-short", map[string]any{"text": "Чтобы знать риски"})
	if at.Status != "pending" {
		t.Fatalf("short answer must be pending: %+v", at)
	}
	got := a.waitAttempt(at.ID)
	if got.Status != "failed" || got.Feedback == nil || !strings.Contains(*got.Feedback, "Не хватает") || got.Hint == nil {
		t.Fatalf("failed short answer: %+v", got)
	}
	_, at = a.answer(course, "q-risks-short", map[string]any{"text": "Чтобы заранее подготовить действия и не терять время"})
	if got = a.waitAttempt(at.ID); got.Status != "passed" {
		t.Fatalf("passing short answer: %+v", got)
	}
	reviews := h.agent.all()[before:]
	if len(reviews) != 2 || reviews[0].SessionDir == reviews[1].SessionDir || reviews[0].ThreadID != "" || reviews[0].Role != agents.WorkReviewer {
		t.Fatalf("each answer needs its own reviewer request: %+v", reviews)
	}
	in := reviews[0].Input
	for _, want := range []string{`"short_answer"`, "зачем заранее составлять список рисков", "меры реагирования", "Чтобы знать риски"} {
		if !strings.Contains(in, want) {
			t.Fatalf("reviewer input lacks %q: %s", want, in)
		}
	}
	if strings.Contains(in, "Ищите формулировку") || strings.Contains(in, "q-goal-single") {
		t.Fatalf("reviewer input mixes in other work: %s", in)
	}
}

func TestReviewerErrorIsNotAPassAndALateResultChangesNothing(t *testing.T) {
	h, g, a, course := readyPM(t)
	h.learn.ReviewTimeout = 200 * time.Millisecond
	release := make(chan struct{})
	var calls int
	var mu sync.Mutex
	g.mu.Lock()
	g.reviewWork = func(req agents.TurnRequest) (agents.TurnResult, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			<-release // answers long after the timeout
			return workReview(false, "Поздний результат"), nil
		}
		return workReview(true, "Зачёт"), nil
	}
	g.mu.Unlock()
	_, first := a.answer(course, "q-risks-short", map[string]any{"text": "первый ответ"})
	if got := a.waitAttempt(first.ID); got.Status != "error" {
		t.Fatalf("timed out review: %+v", got)
	}
	_, second := a.answer(course, "q-risks-short", map[string]any{"text": "второй ответ"})
	if got := a.waitAttempt(second.ID); got.Status != "passed" {
		t.Fatalf("second attempt: %+v", got)
	}
	close(release)
	h.learn.Wait()
	_, b := a.do("GET", "/api/attempts/"+first.ID, nil)
	_, c := a.do("GET", "/api/attempts/"+second.ID, nil)
	if decode[attempt](t, b).Status != "error" || decode[attempt](t, c).Status != "passed" {
		t.Fatalf("late result changed attempts: %s %s", b, c)
	}
}

func TestAttemptsArePrivateAndSurviveRestart(t *testing.T) {
	h, _, a, course := readyPM(t)
	a.answer(course, "q-goal-single", opts("a"))
	other := h.login("b@example.com")
	if st, _ := other.answer(course, "q-goal-single", opts("b")); st != 404 {
		t.Fatalf("other learner answers: %d", st)
	}
	if st, _ := other.do("GET", "/api/courses/"+course+"/attempts", nil); st != 404 {
		t.Fatalf("other learner lists attempts: %d", st)
	}
	_, b := a.do("GET", "/api/courses/"+course+"/attempts", nil)
	first := decode[[]attempt](t, b)
	if st, _ := other.do("GET", "/api/attempts/"+first[0].ID, nil); st != 404 {
		t.Fatalf("other learner reads an attempt: %d", st)
	}
	h.restart()
	a = h.login("a@example.com")
	_, b = a.do("GET", "/api/courses/"+course+"/attempts", nil)
	after := decode[[]attempt](t, b)
	if len(after) != 1 || after[0].Status != "failed" || after[0].Hint == nil {
		t.Fatalf("attempts after restart: %s", b)
	}
}
