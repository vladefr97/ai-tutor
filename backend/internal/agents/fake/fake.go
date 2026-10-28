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
