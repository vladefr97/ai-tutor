package planning

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/vladefr97/tutor-app/backend/internal/agents"
	"github.com/vladefr97/tutor-app/backend/internal/generation"
	"github.com/vladefr97/tutor-app/backend/internal/ops"
)

// Stages shown to the learner while the plan is prepared.
const (
	StageDrafting  = "drafting_plan"
	StageReviewing = "reviewing_plan"
	StageRevising  = "revising_plan"
)

type planningInput struct {
	Skipped    bool `json:"skipped,omitempty"`
	Discussion bool `json:"discussion,omitempty"`
}

type proposal struct {
	Plan        json.RawMessage `json:"plan"`
	Explanation string          `json:"explanation"`
}

type planReview struct {
	Approved bool `json:"approved"`
	Goal     struct {
		OK         bool     `json:"ok"`
		OutOfScope bool     `json:"outOfScope"`
		Issues     []string `json:"issues"`
	} `json:"goal"`
	Plan struct {
		OK     bool     `json:"ok"`
		Issues []string `json:"issues"`
	} `json:"plan"`
}

func bullet(items []string) string { return "- " + strings.Join(items, "\n- ") }

// planning drafts a plan, has every version reviewed in a fresh Plan reviewer
// session and revises it until approval. There is no cycle limit. A goal
// issue sends the Planner back to the learner instead.
func (s *Service) planning(ctx context.Context, op ops.Op) (*ops.Next, error) {
	var in planningInput
	if err := json.Unmarshal(op.Input, &in); err != nil {
		return nil, err
	}
	var direction string
	var goal *string
	var profile json.RawMessage
	if err := s.DB.QueryRow(ctx, `select direction, goal, profile from courses where id = $1`, op.CourseID).
		Scan(&direction, &goal, &profile); err != nil {
		return nil, err
	}
	draft := "[Составь план]"
	switch {
	case in.Skipped:
		draft = "[Ученик пропустил интервью]\nЦель: " + deref(goal) + "\n\n[Составь план]"
	case in.Discussion:
		draft = "[Составь план] Учти изменения, о которых вы договорились с учеником."
	}
	if err := s.Ops.SetStage(ctx, op.ID, StageDrafting); err != nil {
		return nil, err
	}
	var p proposal
	if err := s.plannerTurn(ctx, op.CourseID, draft, agents.KindPlanProposal, &p); err != nil {
		return nil, err
	}
	for {
		if err := s.Ops.SetStage(ctx, op.ID, StageReviewing); err != nil {
			return nil, err
		}
		var versionID string
		if err := s.DB.QueryRow(ctx, `insert into plan_versions (course_id, number, plan, explanation)
			values ($1, (select coalesce(max(number), 0) + 1 from plan_versions where course_id = $1), $2, $3) returning id`,
			op.CourseID, p.Plan, p.Explanation).Scan(&versionID); err != nil {
			return nil, err
		}
		rv, err := s.reviewPlan(ctx, op.CourseID, direction, deref(goal), profile, p.Plan)
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(rv)
		if _, err := s.DB.Exec(ctx, `update plan_versions set review = $2, approved = $3 where id = $1 and review is null`,
			versionID, raw, rv.Approved); err != nil {
			return nil, err
		}
		if rv.Approved {
			_, err := s.DB.Exec(ctx, `update courses set status = 'plan_ready' where id = $1 and status = 'planning'`, op.CourseID)
			return nil, err
		}
		if !rv.Goal.OK || rv.Goal.OutOfScope {
			return nil, s.clarifyGoal(ctx, op.CourseID, rv)
		}
		if err := s.Ops.SetStage(ctx, op.ID, StageRevising); err != nil {
			return nil, err
		}
		fix := "[Ревьюер плана] Замечания к плану:\n" + bullet(rv.Plan.Issues) + "\n\nИсправь план."
		if err := s.plannerTurn(ctx, op.CourseID, fix, agents.KindPlanProposal, &p); err != nil {
			return nil, err
		}
	}
}

// clarifyGoal hands the reviewer's goal issues to the Planner, who asks the
// learner; the course returns to the interview.
func (s *Service) clarifyGoal(ctx context.Context, courseID string, rv planReview) error {
	msg := "[Ревьюер плана] Замечания к цели:\n" + bullet(rv.Goal.Issues)
	if rv.Goal.OutOfScope {
		msg += "\n\nЦель вне экспертной основы направления: объясни границы и предложи ближайшую цель внутри основы."
	}
	msg += "\n\nУточни цель с учеником, предложи понятные варианты. Не упоминай ревьюера."
	var turn interviewTurn
	if err := s.plannerTurn(ctx, courseID, msg, agents.KindInterviewTurn, &turn); err != nil {
		return err
	}
	profile, _ := json.Marshal(turn.Profile)
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into messages (course_id, author, text) values ($1, 'mentor', $2)`, courseID, turn.Message); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `update courses set status = 'interview', profile = $2 where id = $1 and status = 'planning'`, courseID, profile)
		return err
	})
}

// reviewPlan checks goal and plan in one request of a fresh reviewer session.
func (s *Service) reviewPlan(ctx context.Context, courseID, direction, goal string, profile, plan json.RawMessage) (planReview, error) {
	var rv planReview
	var n int
	if err := s.DB.QueryRow(ctx, `select count(*) + 1 from agent_sessions where course_id = $1 and role = $2`,
		courseID, agents.PlanReviewer).Scan(&n); err != nil {
		return rv, err
	}
	dir := s.Config.SessionDir(courseID, agents.PlanReviewer, n)
	model := s.Config.Models[agents.PlanReviewer]
	var sessionID string
	if err := s.DB.QueryRow(ctx, `insert into agent_sessions (course_id, role, dir, model) values ($1, $2, $3, $4) returning id`,
		courseID, agents.PlanReviewer, dir, model).Scan(&sessionID); err != nil {
		return rv, err
	}
	instructions, err := s.Config.Instructions(agents.PlanReviewer, direction)
	if err != nil {
		return rv, err
	}
	input, _ := json.Marshal(map[string]any{"direction": direction, "goal": goal, "profile": profile, "plan": plan})
	res, err := s.Agent.Turn(ctx, agents.TurnRequest{Role: agents.PlanReviewer, SessionDir: dir, Model: model,
		Instructions: instructions, Input: string(input), OutputSchema: agents.OutputSchema(agents.KindPlanReview)})
	if res.ThreadID != "" {
		s.DB.Exec(ctx, `update agent_sessions set thread_id = $2 where id = $1`, sessionID, res.ThreadID)
	}
	if err != nil {
		return rv, reviewFailure(err)
	}
	if err := agents.ParseResult(agents.KindPlanReview, res.Text, &rv); err != nil {
		return rv, reviewFailure(err)
	}
	return rv, nil
}

func reviewFailure(err error) error {
	return &ops.Failure{Code: "review_failed", Message: "Не удалось проверить план. Нажмите «Повторить».", Err: err}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

var ErrVersionMismatch = errors.New("only the latest approved plan version can be confirmed")

// Confirm records the learner's confirmation of a specific approved version.
func (s *Service) Confirm(ctx context.Context, courseID, versionID string) error {
	var started *ops.Op
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var status, direction string
		if err := tx.QueryRow(ctx, `select status, direction from courses where id = $1 for update`, courseID).Scan(&status, &direction); err != nil {
			return err
		}
		if status != "plan_ready" {
			return ErrWrongState
		}
		var latest string
		var approved bool
		err := tx.QueryRow(ctx, `select id, approved from plan_versions where course_id = $1 order by number desc limit 1`, courseID).
			Scan(&latest, &approved)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (latest != versionID || !approved)) {
			return ErrVersionMismatch
		}
		if err != nil {
			return err
		}
		next := "awaiting_github"
		if direction == "pm" {
			next = "generating"
		}
		if _, err = tx.Exec(ctx, `update courses set confirmed_plan_version_id = $2, status = $3 where id = $1`, courseID, versionID, next); err != nil {
			return err
		}
		if direction != "pm" {
			return nil // Python and Git generate after GitHub is connected
		}
		op, err := s.Ops.Start(ctx, tx, courseID, generation.Kind, generation.Input{PlanVersionID: versionID})
		started = &op
		return err
	})
	if err == nil && started != nil {
		s.Ops.Launch(*started)
	}
	return err
}
