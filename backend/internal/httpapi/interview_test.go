package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vladefr97/tutor-app/backend/internal/agents"
)

type operation struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Error  *struct {
		Code, Message string
	} `json:"error"`
}

type courseFull struct {
	ID        string     `json:"id"`
	Direction string     `json:"direction"`
	Status    string     `json:"status"`
	Goal      *string    `json:"goal"`
	Profile   *profile   `json:"profile"`
	Operation *operation `json:"operation"`
}

type profile struct {
	Preparation, Context, Preferences *string
}

type chatMessage struct {
	Author, Text string
}

func turn(message string, goal *string, ready bool, preparation string) agents.TurnResult {
	b, _ := json.Marshal(map[string]any{"kind": "interview_turn", "message": message, "goal": goal,
		"profile": map[string]any{"preparation": nilIfEmpty(preparation), "context": nil, "preferences": nil}, "readyForPlan": ready})
	return agents.TurnResult{ThreadID: "thread-1", Text: string(b)}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func ptr(s string) *string { return &s }

// waitOp polls the operation until it is final.
func (c *client) waitOp(id string) operation {
	c.h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, b := c.do("GET", "/api/operations/"+id, nil)
		op := decode[operation](c.h.t, b)
		if op.Status == "succeeded" || op.Status == "failed" {
			return op
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.h.t.Fatalf("operation %s did not finish", id)
	return operation{}
}

func (c *client) course(id string) courseFull {
	c.h.t.Helper()
	st, b := c.do("GET", "/api/courses/"+id, nil)
	if st != 200 {
		c.h.t.Fatalf("get course: %d %s", st, b)
	}
	return decode[courseFull](c.h.t, b)
}

func (c *client) messages(id string) []chatMessage {
	c.h.t.Helper()
	_, b := c.do("GET", "/api/courses/"+id+"/messages", nil)
	return decode[[]chatMessage](c.h.t, b)
}

// startCourse creates a course and waits for the mentor's greeting.
func (c *client) startCourse(direction string) courseFull {
	c.h.t.Helper()
	st, b := c.do("POST", "/api/courses", map[string]string{"direction": direction})
	if st != 201 {
		c.h.t.Fatalf("create course: %d %s", st, b)
	}
	created := decode[courseFull](c.h.t, b)
	if created.Operation == nil {
		c.h.t.Fatal("new course has no interview operation")
	}
	if op := c.waitOp(created.Operation.ID); op.Status != "succeeded" {
		c.h.t.Fatalf("greeting failed: %+v", op)
	}
	return c.course(created.ID)
}

func (c *client) say(courseID, text string) operation {
	c.h.t.Helper()
	st, b := c.do("POST", "/api/courses/"+courseID+"/messages", map[string]string{"text": text})
	if st != 202 {
		c.h.t.Fatalf("post message: %d %s", st, b)
	}
	return c.waitOp(decode[operation](c.h.t, b).ID)
}

func TestInterviewRunsThroughThePlannerSession(t *testing.T) {
	h := newHarness(t)
	h.agent.set(func(req agents.TurnRequest) (agents.TurnResult, error) {
		switch {
		case strings.HasPrefix(req.Input, "[Начало интервью]"):
			return turn("Какую цель ты хочешь достичь?", nil, false, ""), nil
		case req.Input == "Хочу планировать проекты":
			return turn("Что ты уже умеешь?", ptr("Планировать небольшие проекты"), false, ""), nil
		default:
			return turn("Составляю план.", ptr("Планировать небольшие проекты"), true, "Вёл пару проектов"), nil
		}
	})
	a := h.login("a@example.com")
	c := a.startCourse("pm")
	if op := a.say(c.ID, "Хочу планировать проекты"); op.Status != "succeeded" {
		t.Fatalf("reply: %+v", op)
	}
	if got := a.course(c.ID); got.Status != "interview" || got.Goal == nil || *got.Goal != "Планировать небольшие проекты" {
		t.Fatalf("after first answer: %+v", got)
	}
	a.say(c.ID, "Вёл пару проектов в Excel")
	got := a.course(c.ID)
	if got.Status != "planning" || got.Profile == nil || got.Profile.Preparation == nil || *got.Profile.Preparation != "Вёл пару проектов" {
		t.Fatalf("after interview: %+v", got)
	}
	want := []chatMessage{{"mentor", "Какую цель ты хочешь достичь?"}, {"learner", "Хочу планировать проекты"},
		{"mentor", "Что ты уже умеешь?"}, {"learner", "Вёл пару проектов в Excel"}, {"mentor", "Составляю план."}}
	if msgs := a.messages(c.ID); len(msgs) != len(want) {
		t.Fatalf("messages: %+v", msgs)
	} else {
		for i := range want {
			if msgs[i] != want[i] {
				t.Fatalf("message %d: %+v, want %+v", i, msgs[i], want[i])
			}
		}
	}
	reqs := h.agent.all()
	if len(reqs) != 3 {
		t.Fatalf("agent turns: %d", len(reqs))
	}
	first := reqs[0]
	if first.Role != agents.Planner || first.Model != "planner-model" || first.ThreadID != "" ||
		!strings.Contains(first.Instructions, "ПРОМПТ ПЛАНИРОВЩИКА") || !strings.Contains(first.Instructions, "ОСНОВА PM") {
		t.Fatalf("first turn must start a planner thread with role prompt and expert base: %+v", first)
	}
	if !strings.Contains(string(first.OutputSchema), `"interview_turn"`) {
		t.Fatal("interview turn is not schema-constrained")
	}
	for _, r := range reqs[1:] {
		if r.ThreadID != "thread-1" || r.SessionDir != first.SessionDir {
			t.Fatalf("later turns must resume the saved session: %+v", r)
		}
	}
}

func TestSkippingInterviewSetsDirectionBasicsAndDefaultProfile(t *testing.T) {
	h := newHarness(t)
	h.agent.set(func(agents.TurnRequest) (agents.TurnResult, error) {
		return turn("Какую цель?", nil, false, ""), nil
	})
	a := h.login("a@example.com")
	c := a.startCourse("python")
	st, b := a.do("POST", "/api/courses/"+c.ID+"/interview/skip", nil)
	if st != 200 {
		t.Fatalf("skip: %d %s", st, b)
	}
	got := decode[courseFull](t, b)
	if got.Status != "planning" || got.Goal == nil || *got.Goal != "Основы Python" || got.Profile == nil || got.Profile.Preferences == nil {
		t.Fatalf("after skip: %+v goal=%v profile=%+v", got, *got.Goal, *got.Profile)
	}
	if st, _ := a.do("POST", "/api/courses/"+c.ID+"/interview/skip", nil); st != 409 {
		t.Fatalf("second skip: want 409, got %d", st)
	}
}

func TestSkippingAfterNamingGoalKeepsTheGoal(t *testing.T) {
	h := newHarness(t)
	h.agent.set(func(req agents.TurnRequest) (agents.TurnResult, error) {
		if strings.HasPrefix(req.Input, "[Начало") {
			return turn("Какую цель?", nil, false, ""), nil
		}
		return turn("Что умеешь?", ptr("Автоматизировать отчёты"), false, ""), nil
	})
	a := h.login("a@example.com")
	c := a.startCourse("python")
	a.say(c.ID, "Хочу автоматизировать отчёты")
	_, b := a.do("POST", "/api/courses/"+c.ID+"/interview/skip", nil)
	if got := decode[courseFull](t, b); *got.Goal != "Автоматизировать отчёты" {
		t.Fatalf("goal after skip: %q", *got.Goal)
	}
}

func TestAgentFailureIsAnErrorAndRetryRepeatsTheSameInput(t *testing.T) {
	h := newHarness(t)
	h.agent.set(func(req agents.TurnRequest) (agents.TurnResult, error) {
		if strings.HasPrefix(req.Input, "[Начало") {
			return turn("Какую цель?", nil, false, ""), nil
		}
		return agents.TurnResult{}, errors.New("provider unavailable")
	})
	a := h.login("a@example.com")
	c := a.startCourse("pm")
	op := a.say(c.ID, "Хочу вести проекты")
	if op.Status != "failed" || op.Error == nil || op.Error.Code != "agent_failed" {
		t.Fatalf("want failed operation, got %+v", op)
	}
	if cur := a.course(c.ID).Operation; cur == nil || cur.ID != op.ID || cur.Status != "failed" {
		t.Fatalf("course must expose the failed operation: %+v", cur)
	}
	h.agent.set(func(req agents.TurnRequest) (agents.TurnResult, error) {
		return turn("Что умеешь?", ptr("Вести проекты"), false, ""), nil
	})
	st, b := a.do("POST", "/api/operations/"+op.ID+"/retry", nil)
	if st != 202 {
		t.Fatalf("retry: %d %s", st, b)
	}
	if again := a.waitOp(decode[operation](t, b).ID); again.Status != "succeeded" {
		t.Fatalf("retry result: %+v", again)
	}
	reqs := h.agent.all()
	if last := reqs[len(reqs)-1]; last.Input != "Хочу вести проекты" {
		t.Fatalf("retry input: %q", last.Input)
	}
	learner := 0
	for _, m := range a.messages(c.ID) {
		if m.Author == "learner" {
			learner++
		}
	}
	if learner != 1 {
		t.Fatalf("retry duplicated the learner message: %d", learner)
	}
}

func TestOutputThatViolatesTheSchemaFailsTheTurn(t *testing.T) {
	h := newHarness(t)
	h.agent.set(func(agents.TurnRequest) (agents.TurnResult, error) {
		return agents.TurnResult{ThreadID: "t", Text: `{"kind":"interview_turn","message":"Готово","goal":null,"profile":{"preparation":null,"context":null,"preferences":null},"readyForPlan":true}`}, nil
	})
	a := h.login("a@example.com")
	st, b := a.do("POST", "/api/courses", map[string]string{"direction": "pm"})
	if st != 201 {
		t.Fatal(st)
	}
	c := decode[courseFull](t, b)
	if op := a.waitOp(c.Operation.ID); op.Status != "failed" {
		t.Fatalf("schema violation must fail: %+v", op)
	}
	if got := a.course(c.ID); got.Status != "interview" {
		t.Fatalf("status changed by an invalid turn: %s", got.Status)
	}
}

func TestMessageWhileMentorIsReplyingIsRejected(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	h.agent.set(func(req agents.TurnRequest) (agents.TurnResult, error) {
		if !strings.HasPrefix(req.Input, "[Начало") {
			<-release
		}
		return turn("Дальше?", nil, false, ""), nil
	})
	a := h.login("a@example.com")
	c := a.startCourse("pm")
	if st, _ := a.do("POST", "/api/courses/"+c.ID+"/messages", map[string]string{"text": "раз"}); st != 202 {
		t.Fatal(st)
	}
	if st, _ := a.do("POST", "/api/courses/"+c.ID+"/messages", map[string]string{"text": "два"}); st != 409 {
		t.Fatalf("concurrent message: want 409, got %d", st)
	}
	close(release)
}

func TestInterviewIsPrivateAndSurvivesRestart(t *testing.T) {
	h := newHarness(t)
	h.agent.set(func(req agents.TurnRequest) (agents.TurnResult, error) {
		return turn("Какую цель?", nil, false, "Знает Excel"), nil
	})
	a := h.login("a@example.com")
	c := a.startCourse("pm")
	a.say(c.ID, "Хочу вести проекты")
	b := h.login("b@example.com")
	if st, _ := b.do("GET", "/api/courses/"+c.ID+"/messages", nil); st != 404 {
		t.Fatalf("other learner reads messages: %d", st)
	}
	if st, _ := b.do("POST", "/api/courses/"+c.ID+"/messages", map[string]string{"text": "x"}); st != 404 {
		t.Fatalf("other learner writes messages: %d", st)
	}
	before := a.messages(c.ID)

	h.restart()
	again := h.login("a@example.com")
	if after := again.messages(c.ID); len(after) != len(before) || len(after) != 3 {
		t.Fatalf("history after restart: %+v", after)
	}
	if got := again.course(c.ID); got.Profile == nil || *got.Profile.Preparation != "Знает Excel" {
		t.Fatalf("profile after restart: %+v", got.Profile)
	}
}

func TestOperationInterruptedByRestartIsShownAsFailed(t *testing.T) {
	h := newHarness(t)
	h.agent.set(func(req agents.TurnRequest) (agents.TurnResult, error) {
		return turn("Какую цель?", nil, false, ""), nil
	})
	a := h.login("a@example.com")
	c := a.startCourse("pm")
	// An operation left running by a crashed process:
	var id string
	if err := h.deps.DB.QueryRow(context.Background(), `insert into operations (course_id, kind, status, input)
		values ($1, 'mentor_reply', 'running', '{"text":"x"}') returning id`, c.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	h.restart()
	a = h.login("a@example.com")
	if op := a.waitOp(id); op.Status != "failed" || op.Error.Code != "interrupted" {
		t.Fatalf("interrupted operation: %+v", op)
	}
}
