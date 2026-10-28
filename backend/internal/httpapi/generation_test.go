package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vladefr97/tutor-app/backend/internal/agents"
)

const examples = "../../../contracts/examples/course/valid"

// writeCourse puts a course version into the generator's out/ directory.
func writeCourse(t *testing.T, req agents.TurnRequest, example string, mutate func(pub, priv map[string]any)) {
	t.Helper()
	out := filepath.Join(req.SessionDir, "workspace", "out")
	var pub, priv map[string]any
	raw, _ := os.ReadFile(filepath.Join(examples, example, "course.json"))
	json.Unmarshal(raw, &pub)
	raw, _ = os.ReadFile(filepath.Join(examples, example, "private.json"))
	json.Unmarshal(raw, &priv)
	if mutate != nil {
		mutate(pub, priv)
	}
	for path, v := range map[string]any{"public/course.json": pub, "private/private.json": priv} {
		b, _ := json.MarshalIndent(v, "", "  ")
		os.MkdirAll(filepath.Dir(filepath.Join(out, path)), 0o755)
		if err := os.WriteFile(filepath.Join(out, path), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// genScript plays Planner, Plan reviewer (approve), Generator and Work reviewer.
type genScript struct {
	planningScript
	mu          sync.Mutex
	hasPractice bool
	generate    func(req agents.TurnRequest) (agents.TurnResult, error)
	reviewWork  func(req agents.TurnRequest) (agents.TurnResult, error)
}

func contentReview(approved bool, issues ...string) agents.TurnResult {
	list := []map[string]string{}
	for _, i := range issues {
		list = append(list, map[string]string{"itemId": "q-risks-short", "comment": i})
	}
	b, _ := json.Marshal(map[string]any{"kind": "content_review", "approved": approved, "issues": list})
	return agents.TurnResult{ThreadID: "content-review", Text: string(b)}
}

func (g *genScript) reply(req agents.TurnRequest) (agents.TurnResult, error) {
	g.mu.Lock()
	gen, rw, hasPractice := g.generate, g.reviewWork, g.hasPractice
	g.mu.Unlock()
	switch {
	case req.Role == agents.Generator:
		return gen(req)
	case req.Role == agents.WorkReviewer:
		return rw(req)
	case req.Role == agents.PlanReviewer:
		return approve(), nil
	case isKind(req, agents.KindPlanProposal):
		b, _ := json.Marshal(map[string]any{"kind": "plan_proposal", "explanation": "Пояснение",
			"plan": map[string]any{"title": "Основы", "goal": "Планировать небольшие проекты", "hasPractice": hasPractice,
				"modules": []any{map[string]any{"title": "Цель", "topics": []string{"Цель"}, "practice": []string{}, "outcome": "Умеет"}},
				"exam":    "Тест и кейс"}})
		return agents.TurnResult{ThreadID: "thread-1", Text: string(b)}, nil
	default:
		return g.planningScript.reply(req)
	}
}

func newGenHarness(t *testing.T) (*harness, *genScript) {
	h := newHarness(t)
	g := &genScript{hasPractice: true}
	g.generate = func(req agents.TurnRequest) (agents.TurnResult, error) {
		writeCourse(t, req, "pm-basics", nil)
		return agents.TurnResult{ThreadID: "gen", Text: "Готово"}, nil
	}
	g.reviewWork = func(agents.TurnRequest) (agents.TurnResult, error) { return contentReview(true), nil }
	h.agent.set(g.reply)
	return h, g
}

// confirmPM takes a PM course through interview, planning and confirmation and
// waits for generation to finish.
func (c *client) confirmPM() (courseFull, operation) {
	c.h.t.Helper()
	course, _ := c.interviewToPlan("pm")
	_, v := c.plan(course.ID)
	if st, b := c.do("POST", "/api/courses/"+course.ID+"/plan/confirm", map[string]string{"planVersionId": v.ID}); st != 200 {
		c.h.t.Fatalf("confirm: %d %s", st, b)
	}
	return c.waitPlanning(course.ID)
}

type contentResp struct {
	VersionID string          `json:"versionId"`
	Course    json.RawMessage `json:"course"`
}

func TestGeneratorWorksInACleanSessionWithOnlyTheApprovedPackage(t *testing.T) {
	h, g := newGenHarness(t)
	var inputs []string
	g.generate = func(req agents.TurnRequest) (agents.TurnResult, error) {
		filepath.WalkDir(filepath.Join(req.SessionDir, "workspace"), func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(filepath.Join(req.SessionDir, "workspace"), p)
				inputs = append(inputs, rel)
				raw, _ := os.ReadFile(p)
				if strings.Contains(string(raw), "Хочу планировать проекты") {
					t.Errorf("interview text leaked into %s", rel)
				}
			}
			return nil
		})
		writeCourse(t, req, "pm-basics", nil)
		return agents.TurnResult{ThreadID: "gen", Text: "Готово"}, nil
	}
	a := h.login("a@example.com")
	c, op := a.confirmPM()
	if op.Status != "succeeded" || c.Status != "ready" {
		t.Fatalf("generation: %+v %+v", op, c)
	}
	want := "input/base.md,input/course-private.schema.json,input/course.schema.json,input/plan.json,input/profile.json"
	if got := strings.Join(inputs, ","); got != want {
		t.Fatalf("generator workspace: %s", got)
	}
	var gen, planner agents.TurnRequest
	for _, r := range h.agent.all() {
		switch r.Role {
		case agents.Generator:
			gen = r
		case agents.Planner:
			planner = r
		}
	}
	if gen.ThreadID != "" || gen.SessionDir == planner.SessionDir || gen.Model != "generator-model" ||
		!strings.Contains(gen.Instructions, "ПРОМПТ ГЕНЕРАТОРА") || strings.Contains(gen.Input, "Хочу планировать проекты") {
		t.Fatalf("generator must start a new session without the interview: %+v", gen)
	}
}

func TestPublishedCourseHasNoAnswersAndIsPrivate(t *testing.T) {
	h, _ := newGenHarness(t)
	a := h.login("a@example.com")
	c, _ := a.confirmPM()
	st, b := a.do("GET", "/api/courses/"+c.ID+"/content", nil)
	if st != 200 {
		t.Fatalf("content: %d %s", st, b)
	}
	body := string(b)
	for _, secret := range []string{`"correct"`, `"hint"`, `"explanation"`, `"criteria"`, "Ищите формулировку", "officialChecks"} {
		if strings.Contains(body, secret) {
			t.Fatalf("content leaks %s", secret)
		}
	}
	if got := decode[contentResp](t, b); got.VersionID == "" || !strings.Contains(string(got.Course), "Основы управления проектом") {
		t.Fatalf("content: %s", b)
	}
	if st, _ := h.login("b@example.com").do("GET", "/api/courses/"+c.ID+"/content", nil); st != 404 {
		t.Fatalf("other learner reads content: %d", st)
	}
}

func TestInvalidOutputIsSentBackAndNeverPublished(t *testing.T) {
	h, g := newGenHarness(t)
	var fixes []string
	g.generate = func(req agents.TurnRequest) (agents.TurnResult, error) {
		if strings.HasPrefix(req.Input, "[Проверка структуры]") {
			fixes = append(fixes, req.Input)
		}
		writeCourse(t, req, "pm-basics", func(pub, priv map[string]any) {
			pub["practice"].([]any)[0].(map[string]any)["relatedPages"] = []string{"p-missing"}
		})
		return agents.TurnResult{ThreadID: "gen", Text: "Готово"}, nil
	}
	a := h.login("a@example.com")
	c, op := a.confirmPM()
	if op.Status != "failed" || c.Status != "generation_failed" {
		t.Fatalf("invalid course: %+v %+v", op, c)
	}
	if len(fixes) == 0 || !strings.Contains(fixes[0], "p-missing") {
		t.Fatalf("generator must get the structure problems: %v", fixes)
	}
	if st, _ := a.do("GET", "/api/courses/"+c.ID+"/content", nil); st != 409 {
		t.Fatalf("content of an unpublished course: %d", st)
	}
}

func TestStructureFixIsAccepted(t *testing.T) {
	h, g := newGenHarness(t)
	g.generate = func(req agents.TurnRequest) (agents.TurnResult, error) {
		writeCourse(t, req, "pm-basics", func(pub, priv map[string]any) {
			if !strings.HasPrefix(req.Input, "[Проверка структуры]") {
				delete(priv["questions"].(map[string]any)["q-risks-multi"].(map[string]any), "hint")
			}
		})
		return agents.TurnResult{ThreadID: "gen", Text: "Готово"}, nil
	}
	a := h.login("a@example.com")
	if c, op := a.confirmPM(); op.Status != "succeeded" || c.Status != "ready" {
		t.Fatalf("after fix: %+v %+v", op, c)
	}
}

func TestPlannedPracticeMustBeGenerated(t *testing.T) {
	h, g := newGenHarness(t)
	g.generate = func(req agents.TurnRequest) (agents.TurnResult, error) {
		writeCourse(t, req, "pm-basics", func(pub, priv map[string]any) {
			pub["practice"] = []any{}
			delete(priv["tasks"].(map[string]any), "t-plan-case")
		})
		return agents.TurnResult{ThreadID: "gen", Text: "Готово"}, nil
	}
	a := h.login("a@example.com")
	if c, op := a.confirmPM(); op.Status != "failed" || c.Status == "ready" {
		t.Fatalf("course without planned practice became ready: %+v", c)
	}
}

func TestCourseWithoutPlannedPracticeIsReadyWithTheoryAndExam(t *testing.T) {
	h, g := newGenHarness(t)
	g.hasPractice = false
	g.generate = func(req agents.TurnRequest) (agents.TurnResult, error) {
		writeCourse(t, req, "pm-basics", func(pub, priv map[string]any) {
			pub["practice"] = []any{}
			delete(priv["tasks"].(map[string]any), "t-plan-case")
		})
		return agents.TurnResult{ThreadID: "gen", Text: "Готово"}, nil
	}
	a := h.login("a@example.com")
	if c, op := a.confirmPM(); op.Status != "succeeded" || c.Status != "ready" {
		t.Fatalf("theory-only course: %+v %+v", op, c)
	}
}

func TestContentReviewChangesAreFixedAndReviewedAgain(t *testing.T) {
	h, g := newGenHarness(t)
	reviews := 0
	g.reviewWork = func(req agents.TurnRequest) (agents.TurnResult, error) {
		reviews++
		if _, err := os.Stat(filepath.Join(reviewedOut(t, req), "public", "course.json")); err != nil {
			t.Errorf("reviewer cannot see the version: %v", err)
		}
		if reviews == 1 {
			return contentReview(false, "Критерий слишком узкий"), nil
		}
		return contentReview(true), nil
	}
	var fix string
	g.generate = func(req agents.TurnRequest) (agents.TurnResult, error) {
		if strings.HasPrefix(req.Input, "[Ревьюер работ]") {
			fix = req.Input
		}
		writeCourse(t, req, "pm-basics", nil)
		return agents.TurnResult{ThreadID: "gen", Text: "Готово"}, nil
	}
	a := h.login("a@example.com")
	c, op := a.confirmPM()
	if op.Status != "succeeded" || c.Status != "ready" || reviews != 2 || !strings.Contains(fix, "Критерий слишком узкий") {
		t.Fatalf("review loop: %+v reviews=%d fix=%q", c, reviews, fix)
	}
	var reviewers []agents.TurnRequest
	for _, r := range h.agent.all() {
		if r.Role == agents.WorkReviewer {
			reviewers = append(reviewers, r)
		}
	}
	if len(reviewers) != 2 || reviewers[0].SessionDir == reviewers[1].SessionDir || !strings.Contains(reviewers[0].Input, `"content_review"`) {
		t.Fatalf("each content review must be a fresh session: %+v", reviewers)
	}
}

func TestReviewErrorIsNotApproval(t *testing.T) {
	h, g := newGenHarness(t)
	g.reviewWork = func(agents.TurnRequest) (agents.TurnResult, error) {
		return agents.TurnResult{}, agents.ErrTimeout
	}
	a := h.login("a@example.com")
	if c, op := a.confirmPM(); op.Status != "failed" || c.Status != "generation_failed" {
		t.Fatalf("review timeout: %+v %+v", op, c)
	}
}

func TestApprovalOfAnOlderVersionDoesNotPublishAChangedOne(t *testing.T) {
	h, g := newGenHarness(t)
	reviews := 0
	g.reviewWork = func(req agents.TurnRequest) (agents.TurnResult, error) {
		reviews++
		if reviews == 1 {
			// The version changes after the reviewer read it.
			out := filepath.Join(reviewedOut(t, req), "public", "course.json")
			raw, err := os.ReadFile(out)
			if err != nil {
				t.Errorf("generator output: %v", err)
			}
			os.WriteFile(out, []byte(strings.Replace(string(raw), "Основы управления проектом", "Изменённый курс", 1)), 0o644)
		}
		return contentReview(true), nil
	}
	a := h.login("a@example.com")
	c, op := a.confirmPM()
	if op.Status != "succeeded" || reviews != 2 {
		t.Fatalf("changed version must be reviewed again: %+v reviews=%d", op, reviews)
	}
	_, b := a.do("GET", "/api/courses/"+c.ID+"/content", nil)
	if !strings.Contains(string(b), "Изменённый курс") {
		t.Fatalf("published content is not the reviewed one: %s", b)
	}
}

func TestIncompleteUploadIsNotReady(t *testing.T) {
	h, _ := newGenHarness(t)
	h.store.failPuts = 1
	a := h.login("a@example.com")
	c, op := a.confirmPM()
	if op.Status != "failed" || c.Status == "ready" {
		t.Fatalf("partial upload: %+v %+v", op, c)
	}
	if st, _ := a.do("GET", "/api/courses/"+c.ID+"/content", nil); st != 409 {
		t.Fatalf("content after a failed upload: %d", st)
	}
}

func TestGenerationFailureShowsErrorAndRetryUsesTheSavedPlan(t *testing.T) {
	h, g := newGenHarness(t)
	g.generate = func(req agents.TurnRequest) (agents.TurnResult, error) {
		return agents.TurnResult{}, errors.New("model unavailable")
	}
	a := h.login("a@example.com")
	c, op := a.confirmPM()
	if op.Status != "failed" || c.Status != "generation_failed" || op.Error == nil {
		t.Fatalf("technical failure: %+v %+v", op, c)
	}
	before := len(h.agent.all())
	g.mu.Lock()
	g.generate = func(req agents.TurnRequest) (agents.TurnResult, error) {
		if entries, _ := os.ReadDir(filepath.Join(req.SessionDir, "workspace", "out")); len(entries) > 0 {
			t.Error("retry must start in a clean workspace")
		}
		writeCourse(t, req, "pm-basics", nil)
		return agents.TurnResult{ThreadID: "gen-2", Text: "Готово"}, nil
	}
	g.mu.Unlock()
	if len(h.agent.all()) != before {
		t.Fatal("generation restarted automatically")
	}
	st, b := a.do("POST", "/api/operations/"+op.ID+"/retry", nil)
	if st != 202 {
		t.Fatalf("retry: %d %s", st, b)
	}
	a.waitOp(decode[operation](t, b).ID)
	if got := a.course(c.ID); got.Status != "ready" {
		t.Fatalf("after retry: %+v", got)
	}
	for _, r := range h.agent.all()[before:] {
		if r.Role == agents.Planner {
			t.Fatal("retry must not repeat the interview or planning")
		}
	}
}

func TestMentorFolderIsRestoredFromStorageWithoutServiceFiles(t *testing.T) {
	h, _ := newGenHarness(t)
	a := h.login("a@example.com")
	c, _ := a.confirmPM()
	_, b := a.do("GET", "/api/courses/"+c.ID+"/content", nil)
	version := decode[contentResp](t, b).VersionID
	dir := filepath.Join(t.TempDir(), "course")
	for round := 0; round < 2; round++ {
		if err := h.gen.Materialize(context.Background(), version, dir); err != nil {
			t.Fatal(err)
		}
		var files []string
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(dir, p)
				files = append(files, rel)
				raw, _ := os.ReadFile(p)
				for _, secret := range []string{"Ищите формулировку", "Верно «К 1 марта", "Цель сформулирована как проверяемый"} {
					if strings.Contains(string(raw), secret) {
						t.Errorf("%s contains service text %q", rel, secret)
					}
				}
			}
			return nil
		})
		if got := strings.Join(files, ","); got != "course.json,pages/p-goal.md,pages/p-risks.md" {
			t.Fatalf("mentor folder: %s", got)
		}
		os.RemoveAll(dir) // the local copy is disposable
	}
}

// reviewedOut is the generator output mounted read-only for the Work reviewer.
func reviewedOut(t *testing.T, req agents.TurnRequest) string {
	t.Helper()
	for _, m := range req.Mounts {
		if m.Target == "/workspace/out" && m.ReadOnly {
			return m.HostPath
		}
	}
	t.Fatalf("version is not mounted read-only for the reviewer: %+v", req.Mounts)
	return ""
}
