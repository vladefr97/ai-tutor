// Package fake is a deterministic stand-in for model roles, used by browser
// scenarios (AGENT_FAKE=1). State lives in the session directory, so it
// survives backend restarts like a real thread. Never used for acceptance of
// model behaviour.
package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vladefr97/tutor-app/backend/internal/agents"
	"github.com/vladefr97/tutor-app/contracts"
)

type Agent struct{}

func (Agent) Turn(_ context.Context, req agents.TurnRequest) (agents.TurnResult, error) {
	if err := os.MkdirAll(req.SessionDir, 0o777); err != nil {
		return agents.TurnResult{}, err
	}
	thread := req.ThreadID
	if thread == "" {
		thread = "fake-" + filepath.Base(filepath.Dir(req.SessionDir)) + "-" + filepath.Base(req.SessionDir)
	}
	switch req.Role {
	case agents.Planner:
		if isKind(req, agents.KindPlanProposal) {
			time.Sleep(delay)
			return planProposal(req, thread)
		}
		return planner(req, thread)
	case agents.PlanReviewer:
		time.Sleep(delay)
		return planReview(req, thread)
	case agents.Generator:
		time.Sleep(delay)
		return generate(req, thread)
	case agents.WorkReviewer:
		time.Sleep(delay)
		if isKind(req, agents.KindContentReview) {
			return result(thread, map[string]any{"kind": "content_review", "approved": true, "issues": []any{}})
		}
		return workReview(req, thread)
	case agents.LearningMentor:
		return tutor(req, thread)
	}
	return agents.TurnResult{}, errors.New("fake agent: role " + string(req.Role) + " is not scripted")
}

// failOnce fails the first time a given learner text containing «сбой» is seen.
func failOnce(dir, text string) bool {
	if !strings.Contains(text, "сбой") {
		return false
	}
	h := sha256.Sum256([]byte(text))
	marker := filepath.Join(dir, "failed-"+hex.EncodeToString(h[:8]))
	if _, err := os.Stat(marker); err == nil {
		return false
	}
	os.WriteFile(marker, nil, 0o666)
	return true
}

// delay makes intermediate planning stages visible in the browser.
const delay = 700 * time.Millisecond

func isKind(req agents.TurnRequest, kind string) bool {
	return strings.Contains(string(req.OutputSchema), `"enum":["`+kind+`"]`)
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func result(thread string, v any) (agents.TurnResult, error) {
	b, _ := json.Marshal(v)
	return agents.TurnResult{ThreadID: thread, Text: string(b)}, nil
}

func planner(req agents.TurnRequest, thread string) (agents.TurnResult, error) {
	if failOnce(req.SessionDir, req.Input) {
		return agents.TurnResult{ThreadID: thread}, errors.New("fake: scripted failure")
	}
	goal, _ := os.ReadFile(filepath.Join(req.SessionDir, "goal"))
	if exists(filepath.Join(req.SessionDir, "planned")) && !strings.HasPrefix(req.Input, "[") {
		// Plan discussion: «добавь» agrees on a change, anything else is a question.
		turn := map[string]any{"kind": "interview_turn", "goal": string(goal), "readyForPlan": false,
			"message": "Отвечаю на вопрос: план остаётся прежним.",
			"profile": map[string]any{"preparation": "С нуля", "context": nil, "preferences": nil}}
		if strings.Contains(req.Input, "добавь") {
			turn["message"] = "Хорошо, обновлю план."
			turn["readyForPlan"] = true
			os.WriteFile(filepath.Join(req.SessionDir, "updated"), nil, 0o666)
		}
		return result(thread, turn)
	}
	countFile := filepath.Join(req.SessionDir, "learner-turns")
	raw, _ := os.ReadFile(countFile)
	n, _ := strconv.Atoi(string(raw))
	turn := map[string]any{"kind": "interview_turn", "goal": nil, "readyForPlan": false,
		"profile": map[string]any{"preparation": nil, "context": nil, "preferences": nil}}
	if strings.HasPrefix(req.Input, "[Начало интервью]") {
		turn["message"] = "Привет! Я твой наставник. Какую цель ты хочешь достичь?"
	} else {
		n++
		os.WriteFile(countFile, []byte(strconv.Itoa(n)), 0o666)
		turn["goal"] = "Цель ученика: " + req.Input
		if n == 1 {
			os.WriteFile(filepath.Join(req.SessionDir, "goal"), []byte("Цель ученика: "+req.Input), 0o666)
			turn["message"] = "Понял. Что ты уже умеешь?"
		} else {
			turn["message"] = "Спасибо, этого достаточно. Составляю план."
			turn["readyForPlan"] = true
			turn["profile"] = map[string]any{"preparation": req.Input, "context": nil, "preferences": nil}
		}
	}
	b, _ := json.Marshal(turn)
	return agents.TurnResult{ThreadID: thread, Text: string(b)}, nil
}

func planProposal(req agents.TurnRequest, thread string) (agents.TurnResult, error) {
	if i := strings.Index(req.Input, "Цель: "); strings.HasPrefix(req.Input, "[Ученик пропустил интервью]") && i >= 0 {
		goal := strings.SplitN(req.Input[i+len("Цель: "):], "\n", 2)[0]
		os.WriteFile(filepath.Join(req.SessionDir, "goal"), []byte(goal), 0o666)
	}
	goal, _ := os.ReadFile(filepath.Join(req.SessionDir, "goal"))
	os.WriteFile(filepath.Join(req.SessionDir, "planned"), nil, 0o666)
	module := func(title string) map[string]any {
		return map[string]any{"title": title, "topics": []string{"Тема: " + title}, "practice": []string{"Практика: " + title},
			"outcome": "Результат: " + title}
	}
	title := "План: " + string(goal)
	modules := []any{module("Модуль 1. Основы"), module("Модуль 2. Практика"), module("Модуль 3. Итог")}
	if exists(filepath.Join(req.SessionDir, "updated")) {
		title = "План (обновлён): " + string(goal)
		modules = append(modules, module("Модуль 4. Дополнительно"))
	}
	return result(thread, map[string]any{"kind": "plan_proposal", "explanation": "Объясняю, почему план такой",
		"plan": map[string]any{"title": title, "goal": string(goal), "hasPractice": true, "modules": modules,
			"exam": "Итоговый экзамен: две попытки, тест и практическое задание"}})
}

// planReview asks for changes on the course's first review and approves later ones.
func planReview(req agents.TurnRequest, thread string) (agents.TurnResult, error) {
	first := strings.HasSuffix(req.SessionDir, "-1")
	issues := []string{}
	if first {
		issues = []string{"Добавьте практику к первому модулю"}
	}
	return result(thread, map[string]any{"kind": "plan_review", "approved": !first,
		"goal": map[string]any{"ok": true, "outOfScope": false, "issues": []string{}},
		"plan": map[string]any{"ok": !first, "issues": issues}})
}

// generate writes a valid course: the PM example from contracts, or the
// Python example with official checks and repository files for code tasks.
func generate(req agents.TurnRequest, thread string) (agents.TurnResult, error) {
	out := filepath.Join(req.SessionDir, "workspace", "out")
	example := "pm-basics"
	direction := directionOf(req)
	if direction != "pm" {
		example = "python-functions"
	}
	files := map[string][]byte{}
	for _, name := range []string{"course.json", "private.json"} {
		data, err := contracts.FS.ReadFile("examples/course/valid/" + example + "/" + name)
		if err != nil {
			return agents.TurnResult{}, err
		}
		if name == "course.json" {
			data = []byte(strings.Replace(string(data), `"direction": "python"`, `"direction": "`+direction+`"`, 1))
			files["public/course.json"] = data
		} else {
			files["private/private.json"] = data
		}
	}
	if direction != "pm" {
		files["repo/README.md"] = []byte("# Учебный репозиторий\n\nЗапуск тестов: `python -m pytest`.\n")
		for id, repoPath := range map[string]string{"t-sum-list": "tasks/sum_even", "exam-a-max": "exam/a/max_of", "exam-b-count": "exam/b/count_words"} {
			files["repo/"+repoPath+"/README.md"] = []byte("Условие задания " + id + "\n")
			files["private/checks/"+id+"/test_task.py"] = []byte("def test_placeholder():\n    assert True\n")
		}
	}
	for path, data := range files {
		target := filepath.Join(out, filepath.FromSlash(path))
		os.MkdirAll(filepath.Dir(target), 0o777)
		if err := os.WriteFile(target, data, 0o666); err != nil {
			return agents.TurnResult{}, err
		}
	}
	return agents.TurnResult{ThreadID: thread, Text: "Курс готов."}, nil
}

// directionOf reads the direction from the title of the expert base copied into the workspace.
func directionOf(req agents.TurnRequest) string {
	base, _ := os.ReadFile(filepath.Join(req.SessionDir, "workspace", "input", "base.md"))
	title, _, _ := strings.Cut(string(base), "\n")
	switch {
	case strings.HasSuffix(title, "Git"):
		return "git"
	case strings.HasSuffix(title, "Python"):
		return "python"
	}
	return "pm"
}

// workReview passes a short answer that mentions «заранее».
func workReview(req agents.TurnRequest, thread string) (agents.TurnResult, error) {
	var in struct{ Mode, Answer string }
	json.Unmarshal([]byte(req.Input), &in)
	passed := strings.Contains(in.Answer, "заранее")
	verdict, feedback := "changes_requested", "Не хватает: зачем готовить меры заранее"
	if in.Mode == "case" {
		// A case passes when it names risks and is not a one-liner.
		passed = strings.Contains(strings.ToLower(in.Answer), "риск") && len([]rune(in.Answer)) > 40
		feedback = "Не хватает: риски и меры реагирования"
	}
	if passed {
		verdict, feedback = "passed", "Верно: меры готовят заранее."
		if in.Mode == "case" {
			feedback = "Зачтено: цель, задачи и риски на месте."
		}
	}
	return result(thread, map[string]any{"kind": "work_review", "verdict": verdict, "feedback": feedback,
		"criteria": []any{map[string]any{"criterion": "Меры реагирования готовят заранее", "met": passed, "comment": ""}}})
}

// tutor echoes what the context says, so browser scenarios can see the page
// and the progress the backend passed.
func tutor(req agents.TurnRequest, thread string) (agents.TurnResult, error) {
	raw, _, _ := strings.Cut(strings.TrimPrefix(req.Input, "[Контекст]\n"), "\n\n")
	var ctx struct {
		PageID   string `json:"pageId"`
		Progress []struct {
			Status string `json:"status"`
		} `json:"progress"`
	}
	json.Unmarshal([]byte(raw), &ctx)
	solved := 0
	for _, p := range ctx.Progress {
		if p.Status == "passed" {
			solved++
		}
	}
	return agents.TurnResult{ThreadID: thread, Text: "Наставник: вижу страницу " + ctx.PageID + "; решено вопросов: " + strconv.Itoa(solved)}, nil
}
