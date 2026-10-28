package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vladefr97/tutor-app/backend/internal/agents"
)

type planVersion struct {
	ID          string `json:"id"`
	Number      int    `json:"number"`
	Confirmed   bool   `json:"confirmed"`
	Explanation string `json:"explanation"`
	Plan        struct {
		Title   string
		Goal    string
		Modules []struct{ Title string }
	} `json:"plan"`
}

func proposal(title string) agents.TurnResult {
	b, _ := json.Marshal(map[string]any{"kind": "plan_proposal", "explanation": "Пояснение к " + title,
		"plan": map[string]any{"title": title, "goal": "Планировать небольшие проекты", "hasPractice": true,
			"modules": []any{map[string]any{"title": "Модуль " + title, "topics": []string{"Цель"}, "practice": []string{"Кейс"}, "outcome": "Умеет"}},
			"exam":    "Тест и кейс"}})
	return agents.TurnResult{ThreadID: "thread-1", Text: string(b)}
}

func review(approved, goalOK, outOfScope bool, goalIssues, planIssues []string) agents.TurnResult {
	if goalIssues == nil {
		goalIssues = []string{}
	}
	if planIssues == nil {
		planIssues = []string{}
	}
	b, _ := json.Marshal(map[string]any{"kind": "plan_review", "approved": approved,
		"goal": map[string]any{"ok": goalOK, "outOfScope": outOfScope, "issues": goalIssues},
		"plan": map[string]any{"ok": len(planIssues) == 0, "issues": planIssues}})
	return agents.TurnResult{ThreadID: "review-thread", Text: string(b)}
}

func isKind(req agents.TurnRequest, kind string) bool {
	return strings.Contains(string(req.OutputSchema), `"enum":["`+kind+`"]`)
}

// planningScript plays the Planner and the Plan reviewer. reviews are returned in order.
type planningScript struct {
	mu        sync.Mutex
	reviews   []agents.TurnResult
	proposals int
	discuss   func(req agents.TurnRequest) agents.TurnResult // planner replies after the plan is shown
}

func (p *planningScript) reply(req agents.TurnRequest) (agents.TurnResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case req.Role == agents.PlanReviewer:
		if len(p.reviews) == 0 {
			return agents.TurnResult{}, errors.New("no scripted review")
		}
		r := p.reviews[0]
		p.reviews = p.reviews[1:]
		return r, nil
	case isKind(req, agents.KindPlanProposal):
		p.proposals++
		return proposal(fmt.Sprintf("v%d", p.proposals)), nil
	case strings.HasPrefix(req.Input, "[Начало интервью]"):
		return turn("Какую цель ты хочешь достичь?", nil, false, ""), nil
	case strings.HasPrefix(req.Input, "[Ревьюер плана]"):
		return turn("Давай уточним цель. Вариант А или Б?", ptr("Планировать проекты"), false, ""), nil
	case p.discuss != nil:
		return p.discuss(req), nil
	default:
		return turn("Составляю план.", ptr("Планировать небольшие проекты"), true, "С нуля"), nil
	}
}

func (p *planningScript) queueReviews(r ...agents.TurnResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reviews = append(p.reviews, r...)
}

func approve() agents.TurnResult { return review(true, true, false, nil, nil) }

// interviewToPlan runs a PM interview to the point where planning starts and waits for it.
func (c *client) interviewToPlan(direction string) (courseFull, operation) {
	c.h.t.Helper()
	course := c.startCourse(direction)
	st, b := c.do("POST", "/api/courses/"+course.ID+"/messages", map[string]string{"text": "Хочу планировать проекты"})
	if st != 202 {
		c.h.t.Fatalf("message: %d %s", st, b)
	}
	c.waitOp(decode[operation](c.h.t, b).ID)
	return c.waitPlanning(course.ID)
}

// waitPlanning waits until the course has no active operation and returns the
// last operation's outcome (a succeeded one is no longer exposed as current).
func (c *client) waitPlanning(courseID string) (courseFull, operation) {
	c.h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got := c.course(courseID)
		if got.Operation == nil {
			return got, operation{Status: "succeeded"}
		}
		if got.Operation.Status == "failed" {
			return got, *got.Operation
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.h.t.Fatalf("planning of %s did not finish", courseID)
	return courseFull{}, operation{}
}

func (c *client) plan(courseID string) (int, planVersion) {
	c.h.t.Helper()
	st, b := c.do("GET", "/api/courses/"+courseID+"/plan", nil)
	if st != 200 {
		return st, planVersion{}
	}
	return st, decode[planVersion](c.h.t, b)
}

func newPlanningHarness(t *testing.T) (*harness, *planningScript) {
	h := newHarness(t)
	s := &planningScript{}
	h.agent.set(s.reply)
	return h, s
}

func TestPlanIsReviewedRevisedAndShownOnlyAfterApproval(t *testing.T) {
	h, s := newPlanningHarness(t)
	a := h.login("a@example.com")
	var stages []string
	var stagesMu sync.Mutex
	h.agent.set(func(req agents.TurnRequest) (agents.TurnResult, error) {
		if req.Role == agents.PlanReviewer || isKind(req, agents.KindPlanProposal) {
			var stage string
			a.h.db.QueryRow(context.Background(), `select coalesce(stage, '') from operations where status = 'running'`).Scan(&stage)
			stagesMu.Lock()
			stages = append(stages, stage)
			stagesMu.Unlock()
		}
		return s.reply(req)
	})
	s.queueReviews(review(false, true, false, nil, []string{"Нет практики по рискам"}), approve())
	c, op := a.interviewToPlan("pm")
	if op.Status != "succeeded" || c.Status != "plan_ready" {
		t.Fatalf("planning: %+v %+v", op, c)
	}
	if strings.Join(stages, ",") != "drafting_plan,reviewing_plan,revising_plan,reviewing_plan" {
		t.Fatalf("stages: %v", stages)
	}
	st, v := a.plan(c.ID)
	if st != 200 || v.Number != 2 || v.Plan.Title != "v2" || v.Confirmed || v.Explanation != "Пояснение к v2" {
		t.Fatalf("plan shown to learner: %d %+v", st, v)
	}
	var reviewers []agents.TurnRequest
	for _, r := range h.agent.all() {
		if r.Role == agents.PlanReviewer {
			reviewers = append(reviewers, r)
		}
	}
	if len(reviewers) != 2 || reviewers[0].SessionDir == reviewers[1].SessionDir || reviewers[0].ThreadID != "" {
		t.Fatalf("each review must be a fresh session: %+v", reviewers)
	}
	for _, r := range reviewers {
		if !strings.Contains(r.Input, "Планировать небольшие проекты") || !strings.Contains(r.Input, `"modules"`) ||
			r.Model != "reviewer-model" || !strings.Contains(r.Instructions, "ПРОМПТ РЕВЬЮЕРА ПЛАНА") {
			t.Fatalf("reviewer must get goal and plan in one request: %+v", r)
		}
	}
	// Every version is stored with its review result.
	var n, approved int
	h.db.QueryRow(context.Background(), `select count(*), count(*) filter (where approved) from plan_versions
		where course_id = $1 and review is not null`, c.ID).Scan(&n, &approved)
	if n != 2 || approved != 1 {
		t.Fatalf("stored versions: %d reviewed, %d approved", n, approved)
	}
	// The learner never sees the reviewer.
	for _, m := range a.messages(c.ID) {
		if strings.Contains(m.Text, "Ревьюер") {
			t.Fatalf("reviewer leaked to the learner: %q", m.Text)
		}
	}
}

func TestFourthReviewCycleIsNotAnError(t *testing.T) {
	h, s := newPlanningHarness(t)
	issue := review(false, true, false, nil, []string{"Поправь"})
	s.queueReviews(issue, issue, issue, approve())
	a := h.login("a@example.com")
	c, op := a.interviewToPlan("pm")
	if op.Status != "succeeded" || c.Status != "plan_ready" {
		t.Fatalf("fourth cycle: %+v %+v", op, c)
	}
	if _, v := a.plan(c.ID); v.Number != 4 {
		t.Fatalf("version: %d", v.Number)
	}
}

func TestGoalIssueReturnsToTheLearnerWithOptions(t *testing.T) {
	h, s := newPlanningHarness(t)
	s.queueReviews(review(false, false, false, []string{"Непонятно, что ученик сможет делать"}, nil))
	a := h.login("a@example.com")
	c, op := a.interviewToPlan("pm")
	if op.Status != "succeeded" || c.Status != "interview" {
		t.Fatalf("goal issue: %+v %+v", op, c)
	}
	if st, _ := a.plan(c.ID); st != 404 {
		t.Fatalf("plan must not be shown: %d", st)
	}
	msgs := a.messages(c.ID)
	if last := msgs[len(msgs)-1]; last.Author != "mentor" || !strings.Contains(last.Text, "Вариант А или Б") {
		t.Fatalf("mentor must ask the learner: %+v", last)
	}
	var toPlanner string
	for _, r := range h.agent.all() {
		if strings.HasPrefix(r.Input, "[Ревьюер плана]") {
			toPlanner = r.Input
		}
	}
	if !strings.Contains(toPlanner, "Непонятно, что ученик сможет делать") || !isKindIn(h, "[Ревьюер плана]", agents.KindInterviewTurn) {
		t.Fatalf("planner must get the goal issues and answer the learner: %q", toPlanner)
	}
	// The clarified goal goes through planning again.
	s.queueReviews(approve())
	st, b := a.do("POST", "/api/courses/"+c.ID+"/messages", map[string]string{"text": "Вариант А"})
	if st != 202 {
		t.Fatal(st)
	}
	a.waitOp(decode[operation](t, b).ID)
	if c, _ = a.waitPlanning(c.ID); c.Status != "plan_ready" {
		t.Fatalf("after clarification: %+v", c)
	}
}

func isKindIn(h *harness, prefix, kind string) bool {
	for _, r := range h.agent.all() {
		if strings.HasPrefix(r.Input, prefix) && isKind(r, kind) {
			return true
		}
	}
	return false
}

func TestGoalOutsideTheBaseDoesNotProduceAPlan(t *testing.T) {
	h, s := newPlanningHarness(t)
	s.queueReviews(review(false, false, true, []string{"Цель про машинное обучение вне основы"}, nil))
	a := h.login("a@example.com")
	c, _ := a.interviewToPlan("python")
	if c.Status != "interview" {
		t.Fatalf("out of scope: %+v", c)
	}
	if st, _ := a.plan(c.ID); st != 404 {
		t.Fatalf("plan for an out-of-scope goal: %d", st)
	}
}

func TestLearnerConfirmsTheExactApprovedVersion(t *testing.T) {
	h, s := newPlanningHarness(t)
	s.queueReviews(approve())
	a := h.login("a@example.com")
	c, _ := a.interviewToPlan("pm")
	_, v := a.plan(c.ID)
	if st, _ := a.do("POST", "/api/courses/"+c.ID+"/plan/confirm", map[string]string{"planVersionId": "00000000-0000-0000-0000-000000000000"}); st != 409 {
		t.Fatalf("confirm unknown version: %d", st)
	}
	other := h.login("b@example.com")
	if st, _ := other.do("POST", "/api/courses/"+c.ID+"/plan/confirm", map[string]string{"planVersionId": v.ID}); st != 404 {
		t.Fatalf("other learner confirms: %d", st)
	}
	st, b := a.do("POST", "/api/courses/"+c.ID+"/plan/confirm", map[string]string{"planVersionId": v.ID})
	if st != 200 {
		t.Fatalf("confirm: %d %s", st, b)
	}
	var got struct {
		Status                 string
		ConfirmedPlanVersionID *string `json:"confirmedPlanVersionId"`
	}
	json.Unmarshal(b, &got)
	if got.ConfirmedPlanVersionID == nil || *got.ConfirmedPlanVersionID != v.ID || got.Status == "plan_ready" {
		t.Fatalf("after confirm: %+v", got)
	}
	if _, v2 := a.plan(c.ID); !v2.Confirmed {
		t.Fatal("version must be marked confirmed")
	}
	if st, _ := a.do("POST", "/api/courses/"+c.ID+"/plan/confirm", map[string]string{"planVersionId": v.ID}); st != 409 {
		t.Fatalf("second confirm: %d", st)
	}
}

func TestPythonAndGitWaitForGitHubAfterConfirmation(t *testing.T) {
	h, s := newPlanningHarness(t)
	s.queueReviews(approve())
	a := h.login("a@example.com")
	c, _ := a.interviewToPlan("python")
	_, v := a.plan(c.ID)
	_, b := a.do("POST", "/api/courses/"+c.ID+"/plan/confirm", map[string]string{"planVersionId": v.ID})
	if got := decode[courseFull](t, b); got.Status != "awaiting_github" {
		t.Fatalf("python after confirm: %s", got.Status)
	}
}

func TestChangedPlanIsANewVersionThatNeedsReviewAndInvalidatesTheOld(t *testing.T) {
	h, s := newPlanningHarness(t)
	s.queueReviews(approve())
	a := h.login("a@example.com")
	c, _ := a.interviewToPlan("pm")
	_, v1 := a.plan(c.ID)
	s.mu.Lock()
	s.discuss = func(req agents.TurnRequest) agents.TurnResult {
		if strings.Contains(req.Input, "добавь") {
			return turn("Хорошо, обновлю план.", ptr("Планировать небольшие проекты"), true, "С нуля")
		}
		return turn("Риски разберём во втором модуле.", ptr("Планировать небольшие проекты"), false, "С нуля")
	}
	s.mu.Unlock()

	// A question does not change the plan.
	st, b := a.do("POST", "/api/courses/"+c.ID+"/messages", map[string]string{"text": "А где риски?"})
	if st != 202 {
		t.Fatalf("discussion message: %d %s", st, b)
	}
	a.waitOp(decode[operation](t, b).ID)
	if got := a.course(c.ID); got.Status != "plan_ready" {
		t.Fatalf("question changed state: %s", got.Status)
	}
	if _, v := a.plan(c.ID); v.ID != v1.ID {
		t.Fatal("question created a new version")
	}

	// A change request produces a new version that is reviewed again.
	release := make(chan struct{})
	h.agent.set(func(req agents.TurnRequest) (agents.TurnResult, error) {
		if req.Role == agents.PlanReviewer {
			<-release
		}
		return s.reply(req)
	})
	s.queueReviews(approve())
	_, b = a.do("POST", "/api/courses/"+c.ID+"/messages", map[string]string{"text": "добавь кейс про бюджет"})
	a.waitOp(decode[operation](t, b).ID)
	during := a.course(c.ID)
	if during.Status != "planning" {
		t.Fatalf("while the new version is reviewed: %s", during.Status)
	}
	if st, _ := a.do("POST", "/api/courses/"+c.ID+"/plan/confirm", map[string]string{"planVersionId": v1.ID}); st != 409 {
		t.Fatalf("old version confirmed during re-review: %d", st)
	}
	close(release)
	got, _ := a.waitPlanning(c.ID)
	_, v2 := a.plan(c.ID)
	if got.Status != "plan_ready" || v2.ID == v1.ID || v2.Number != 2 {
		t.Fatalf("new version: %+v %+v", got, v2)
	}
	if st, _ := a.do("POST", "/api/courses/"+c.ID+"/plan/confirm", map[string]string{"planVersionId": v1.ID}); st != 409 {
		t.Fatalf("superseded version confirmed: %d", st)
	}
	if st, _ := a.do("POST", "/api/courses/"+c.ID+"/plan/confirm", map[string]string{"planVersionId": v2.ID}); st != 200 {
		t.Fatalf("current version: %d", st)
	}
}

func TestReviewerFailureIsNotApprovalAndRetryRunsPlanningAgain(t *testing.T) {
	h, s := newPlanningHarness(t) // no reviews queued: the reviewer fails
	a := h.login("a@example.com")
	c, op := a.interviewToPlan("pm")
	if op.Status != "failed" || c.Status != "planning" {
		t.Fatalf("reviewer failure: %+v %+v", op, c)
	}
	if st, _ := a.plan(c.ID); st != 404 {
		t.Fatal("unreviewed plan shown")
	}
	s.queueReviews(approve())
	st, b := a.do("POST", "/api/operations/"+op.ID+"/retry", nil)
	if st != 202 {
		t.Fatalf("retry: %d %s", st, b)
	}
	if again := a.waitOp(decode[operation](t, b).ID); again.Status != "succeeded" || a.course(c.ID).Status != "plan_ready" {
		t.Fatalf("retry: %+v", again)
	}
}

func TestSkippedInterviewGoesStraightToPlanning(t *testing.T) {
	h, s := newPlanningHarness(t)
	s.queueReviews(approve())
	a := h.login("a@example.com")
	c := a.startCourse("git")
	st, b := a.do("POST", "/api/courses/"+c.ID+"/interview/skip", nil)
	if st != 200 || decode[courseFull](t, b).Operation == nil {
		t.Fatalf("skip must start planning: %d %s", st, b)
	}
	if got, _ := a.waitPlanning(c.ID); got.Status != "plan_ready" {
		t.Fatalf("after skip: %+v", got)
	}
	var drafted string
	for _, r := range h.agent.all() {
		if isKind(r, agents.KindPlanProposal) {
			drafted = r.Input
		}
	}
	if !strings.Contains(drafted, "[Ученик пропустил интервью]") || !strings.Contains(drafted, "Основы Git") {
		t.Fatalf("planner must know the interview was skipped: %q", drafted)
	}
}
