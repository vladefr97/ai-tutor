package httpapi_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vladefr97/tutor-app/backend/internal/agents"
)

// mentorScript answers Learning Mentor turns with fn and everything else with the course script.
func mentorScript(h *harness, g *genScript, fn func(req agents.TurnRequest) (agents.TurnResult, error)) {
	h.agent.set(func(req agents.TurnRequest) (agents.TurnResult, error) {
		if req.Role == agents.LearningMentor {
			return fn(req)
		}
		return g.reply(req)
	})
}

func (c *client) chat(courseID, text, pageID string) operation {
	c.h.t.Helper()
	st, b := c.do("POST", "/api/courses/"+courseID+"/chat", map[string]string{"text": text, "pageId": pageID})
	if st != 202 {
		c.h.t.Fatalf("chat: %d %s", st, b)
	}
	return c.waitOp(decode[operation](c.h.t, b).ID)
}

func (c *client) chatHistory(courseID string) []chatMessage {
	c.h.t.Helper()
	st, b := c.do("GET", "/api/courses/"+courseID+"/chat", nil)
	if st != 200 {
		c.h.t.Fatalf("chat history: %d %s", st, b)
	}
	return decode[[]chatMessage](c.h.t, b)
}

func courseMount(t *testing.T, req agents.TurnRequest) string {
	t.Helper()
	for _, m := range req.Mounts {
		if m.Target == "/course" && m.ReadOnly {
			return m.HostPath
		}
	}
	t.Fatalf("course folder is not mounted read-only: %+v", req.Mounts)
	return ""
}

func TestMentorWorksFromAReadOnlyFolderWithOnlyPublicMaterials(t *testing.T) {
	h, g, a, course := readyPM(t)
	var got agents.TurnRequest
	var files []string
	mentorScript(h, g, func(req agents.TurnRequest) (agents.TurnResult, error) {
		got = req
		dir := courseMount(t, req)
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(dir, p)
				files = append(files, rel)
				raw, _ := os.ReadFile(p)
				for _, secret := range []string{"Ищите формулировку", "Верно «К 1 марта", "Цель сформулирована как проверяемый", `"correct"`, "criteria"} {
					if strings.Contains(string(raw), secret) {
						t.Errorf("%s contains service data %q", rel, secret)
					}
				}
			}
			return nil
		})
		return agents.TurnResult{ThreadID: "mentor-thread", Text: "Смотри страницу «Риски»."}, nil
	})
	if op := a.chat(course, "Что такое риск?", "p-goal"); op.Status != "succeeded" {
		t.Fatalf("chat: %+v", op)
	}
	if strings.Join(files, ",") != "course.json,pages/p-goal.md,pages/p-risks.md" {
		t.Fatalf("mentor folder: %v", files)
	}
	if got.Model != "mentor-model" || got.OutputSchema != nil || !strings.Contains(got.Instructions, "ПРОМПТ НАСТАВНИКА") ||
		!strings.Contains(got.Instructions, "ОСНОВА PM") {
		t.Fatalf("mentor session: %+v", got)
	}
	for _, want := range []string{"[Контекст]", `"pageId":"p-goal"`, `"exam":false`, "Что такое риск?"} {
		if !strings.Contains(got.Input, want) {
			t.Fatalf("turn input lacks %q: %s", want, got.Input)
		}
	}
	want := []chatMessage{{"learner", "Что такое риск?"}, {"mentor", "Смотри страницу «Риски»."}}
	if hist := a.chatHistory(course); len(hist) != 2 || hist[0] != want[0] || hist[1] != want[1] {
		t.Fatalf("history: %+v", hist)
	}
	for _, m := range a.messages(course) {
		if m.Text == "Что такое риск?" {
			t.Fatal("chat leaked into the interview history")
		}
	}
}

func TestNextTurnSeesTheLatestAttempts(t *testing.T) {
	h, g, a, course := readyPM(t)
	var mu sync.Mutex
	var inputs []string
	mentorScript(h, g, func(req agents.TurnRequest) (agents.TurnResult, error) {
		mu.Lock()
		inputs = append(inputs, req.Input)
		mu.Unlock()
		return agents.TurnResult{ThreadID: "mentor-thread", Text: "Ок"}, nil
	})
	a.chat(course, "Привет", "p-goal")
	a.answer(course, "q-goal-single", opts("b"))
	a.chat(course, "Я ответил", "p-goal")
	if strings.Contains(inputs[0], "q-goal-single") || !strings.Contains(inputs[1], `"itemId":"q-goal-single"`) ||
		!strings.Contains(inputs[1], `"status":"passed"`) {
		t.Fatalf("progress in context:\n%s\n%s", inputs[0], inputs[1])
	}
	var threads []string
	for _, r := range h.agent.all() {
		if r.Role == agents.LearningMentor {
			threads = append(threads, r.ThreadID)
		}
	}
	if len(threads) != 2 || threads[0] != "" || threads[1] != "mentor-thread" {
		t.Fatalf("mentor session must persist its thread: %v", threads)
	}
}

func TestDeletedFolderIsRestoredFromTheVersionTheSiteShows(t *testing.T) {
	h, g, a, course := readyPM(t)
	var dirs []string
	var restored string
	mentorScript(h, g, func(req agents.TurnRequest) (agents.TurnResult, error) {
		dir := courseMount(t, req)
		dirs = append(dirs, dir)
		raw, _ := os.ReadFile(filepath.Join(dir, "course.json"))
		restored = string(raw)
		return agents.TurnResult{ThreadID: "mentor-thread", Text: "Ок"}, nil
	})
	a.chat(course, "Раз", "p-goal")
	if err := os.RemoveAll(dirs[0]); err != nil {
		t.Fatal(err)
	}
	restored = ""
	a.chat(course, "Два", "p-goal")
	_, b := a.do("GET", "/api/courses/"+course+"/content", nil)
	site := decode[contentResp](t, b)
	if restored == "" || !strings.Contains(restored, "Основы управления проектом") {
		t.Fatalf("folder was not restored: %q", restored)
	}
	if !strings.Contains(dirs[1], site.VersionID) {
		t.Fatalf("folder must belong to the site version %s: %s", site.VersionID, dirs[1])
	}
}

func TestChatIsPrivateSurvivesRestartAndNeedsAReadyCourse(t *testing.T) {
	h, g, a, course := readyPM(t)
	mentorScript(h, g, func(req agents.TurnRequest) (agents.TurnResult, error) {
		return agents.TurnResult{ThreadID: "mentor-thread", Text: "Ответ"}, nil
	})
	a.chat(course, "Вопрос", "p-goal")
	other := h.login("b@example.com")
	if st, _ := other.do("GET", "/api/courses/"+course+"/chat", nil); st != 404 {
		t.Fatalf("other learner reads chat: %d", st)
	}
	if st, _ := other.do("POST", "/api/courses/"+course+"/chat", map[string]string{"text": "x", "pageId": "p-goal"}); st != 404 {
		t.Fatalf("other learner writes chat: %d", st)
	}
	h.restart()
	a = h.login("a@example.com")
	if hist := a.chatHistory(course); len(hist) != 2 {
		t.Fatalf("history after restart: %+v", hist)
	}
	fresh := a.startCourse("pm")
	if st, _ := a.do("POST", "/api/courses/"+fresh.ID+"/chat", map[string]string{"text": "x", "pageId": "p-goal"}); st != 409 {
		t.Fatalf("chat before the course is ready: %d", st)
	}
}

func TestMentorFailureIsShownAndRetried(t *testing.T) {
	h, g, a, course := readyPM(t)
	fail := true
	var mu sync.Mutex
	mentorScript(h, g, func(req agents.TurnRequest) (agents.TurnResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			fail = false
			return agents.TurnResult{}, errors.New("provider down")
		}
		return agents.TurnResult{ThreadID: "mentor-thread", Text: "Теперь ответ"}, nil
	})
	op := a.chat(course, "Вопрос", "p-goal")
	if op.Status != "failed" || op.Error == nil {
		t.Fatalf("failure: %+v", op)
	}
	st, b := a.do("POST", "/api/operations/"+op.ID+"/retry", nil)
	if st != 202 {
		t.Fatalf("retry: %d %s", st, b)
	}
	a.waitOp(decode[operation](t, b).ID)
	hist := a.chatHistory(course)
	if len(hist) != 2 || hist[1].Text != "Теперь ответ" {
		t.Fatalf("after retry: %+v", hist)
	}
}
