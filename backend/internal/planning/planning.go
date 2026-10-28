// Package planning runs the interview with the Planner mentor and, later,
// plan drafting and review. Each step is a background operation.
package planning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vladefr97/tutor-app/backend/internal/agents"
	"github.com/vladefr97/tutor-app/backend/internal/ops"
)

const (
	KindMentorReply = "mentor_reply"
	KindPlanning    = "planning"
)

var ErrWrongState = errors.New("action is not allowed in the current course state")

type Profile struct {
	Preparation *string `json:"preparation"`
	Context     *string `json:"context"`
	Preferences *string `json:"preferences"`
}

var DirectionTitles = map[string]string{"python": "Python", "git": "Git", "pm": "управления проектами"}

// DefaultProfile is used when the interview is skipped.
func DefaultProfile() Profile {
	p, c, pref := "Начинает с нуля", "Не уточнялся", "На «вы», дружелюбно, простыми словами"
	return Profile{&p, &c, &pref}
}

type Service struct {
	DB     *pgxpool.Pool
	Agent  agents.Agent
	Config agents.Config
	Ops    *ops.Runner
}

func (s *Service) Register() {
	s.Ops.Handle(KindMentorReply, s.mentorReply)
	s.Ops.Handle(KindPlanning, s.planning)
}

type replyInput struct {
	Start bool   `json:"start,omitempty"`
	Text  string `json:"text,omitempty"`
}

// StartInterview queues the mentor's first message for a new course.
func (s *Service) StartInterview(ctx context.Context, courseID string) (ops.Op, error) {
	return s.Ops.Start(ctx, nil, courseID, KindMentorReply, replyInput{Start: true})
}

// LearnerMessage stores the learner's message and queues the mentor's reply.
func (s *Service) LearnerMessage(ctx context.Context, courseID, text string) (ops.Op, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return ops.Op{}, err
	}
	defer tx.Rollback(ctx)
	var status string
	if err := tx.QueryRow(ctx, `select status from courses where id = $1 for update`, courseID).Scan(&status); err != nil {
		return ops.Op{}, err
	}
	if status != "interview" && status != "plan_ready" {
		return ops.Op{}, ErrWrongState
	}
	op, err := s.Ops.Start(ctx, tx, courseID, KindMentorReply, replyInput{Text: text})
	if err != nil {
		return op, err
	}
	if _, err := tx.Exec(ctx, `insert into messages (course_id, author, text) values ($1, 'learner', $2)`, courseID, text); err != nil {
		return op, err
	}
	if err := tx.Commit(ctx); err != nil {
		return op, err
	}
	s.Ops.Launch(op)
	return op, nil
}

// SkipInterview keeps a goal the learner already named, otherwise sets the
// direction basics, fills missing profile fields with defaults and starts planning.
func (s *Service) SkipInterview(ctx context.Context, courseID string) (ops.Op, error) {
	def, _ := json.Marshal(DefaultProfile())
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return ops.Op{}, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `update courses set status = 'planning',
		goal = coalesce(goal, 'Основы ' || $2::text),
		profile = $3::jsonb || jsonb_strip_nulls(coalesce(profile, '{}'))
		where id = $1 and status = 'interview'
		and not exists (select 1 from operations where course_id = $1 and status in ('queued', 'running'))`,
		courseID, DirectionTitles[s.direction(ctx, courseID)], def)
	if err != nil {
		return ops.Op{}, err
	}
	if tag.RowsAffected() == 0 {
		return ops.Op{}, ErrWrongState
	}
	op, err := s.Ops.Start(ctx, tx, courseID, KindPlanning, planningInput{Skipped: true})
	if err != nil {
		return op, err
	}
	if err := tx.Commit(ctx); err != nil {
		return op, err
	}
	s.Ops.Launch(op)
	return op, nil
}

func (s *Service) direction(ctx context.Context, courseID string) string {
	var d string
	s.DB.QueryRow(ctx, `select direction from courses where id = $1`, courseID).Scan(&d)
	return d
}

type session struct {
	ID, Dir, Model string
	ThreadID       *string
}

// plannerSession returns the course's Planner session, creating it on first use.
func (s *Service) plannerSession(ctx context.Context, courseID string) (session, error) {
	var ss session
	err := s.DB.QueryRow(ctx, `select id, dir, model, thread_id from agent_sessions
		where course_id = $1 and role = $2 order by created_at desc limit 1`, courseID, agents.Planner).
		Scan(&ss.ID, &ss.Dir, &ss.Model, &ss.ThreadID)
	if err == nil {
		return ss, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ss, err
	}
	ss.Dir = s.Config.SessionDir(courseID, agents.Planner, 1)
	ss.Model = s.Config.Models[agents.Planner]
	err = s.DB.QueryRow(ctx, `insert into agent_sessions (course_id, role, dir, model) values ($1, $2, $3, $4) returning id`,
		courseID, agents.Planner, ss.Dir, ss.Model).Scan(&ss.ID)
	return ss, err
}

type interviewTurn struct {
	Message      string  `json:"message"`
	Goal         *string `json:"goal"`
	Profile      Profile `json:"profile"`
	ReadyForPlan bool    `json:"readyForPlan"`
}

func agentFailure(err error) error {
	if errors.Is(err, agents.ErrTimeout) {
		return &ops.Failure{Code: "agent_timeout", Message: "Наставник не ответил вовремя. Нажмите «Повторить».", Err: err}
	}
	return &ops.Failure{Code: "agent_failed", Message: "Наставник не ответил. Нажмите «Повторить».", Err: err}
}

func (s *Service) mentorReply(ctx context.Context, op ops.Op) (*ops.Next, error) {
	var in replyInput
	if err := json.Unmarshal(op.Input, &in); err != nil {
		return nil, err
	}
	input := in.Text
	if in.Start {
		input = fmt.Sprintf("[Начало интервью]\nНаправление: %s", s.direction(ctx, op.CourseID))
	}
	var turn interviewTurn
	if err := s.plannerTurn(ctx, op.CourseID, input, agents.KindInterviewTurn, &turn); err != nil {
		return nil, err
	}
	var next *ops.Next
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `select status from courses where id = $1 for update`, op.CourseID).Scan(&status); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into messages (course_id, author, text) values ($1, 'mentor', $2)`, op.CourseID, turn.Message); err != nil {
			return err
		}
		if status != "interview" && status != "plan_ready" {
			return nil
		}
		// In the interview readyForPlan means the goal is clear; in the plan
		// discussion it means the learner agreed on changes: both need a new,
		// reviewed version.
		profile, _ := json.Marshal(turn.Profile)
		next = nil
		newStatus := status
		if turn.ReadyForPlan {
			newStatus = "planning"
			next = &ops.Next{Kind: KindPlanning, Input: planningInput{Discussion: status == "plan_ready"}}
		}
		_, err := tx.Exec(ctx, `update courses set goal = coalesce($2, goal), profile = $3, status = $4 where id = $1`,
			op.CourseID, turn.Goal, profile, newStatus)
		return err
	})
	return next, err
}

// plannerTurn runs one turn in the course's Planner session and decodes the result.
func (s *Service) plannerTurn(ctx context.Context, courseID, input, kind string, into any) error {
	ss, err := s.plannerSession(ctx, courseID)
	if err != nil {
		return err
	}
	req := agents.TurnRequest{Role: agents.Planner, SessionDir: ss.Dir, Model: ss.Model,
		OutputSchema: agents.OutputSchema(kind), Input: input}
	if ss.ThreadID != nil {
		req.ThreadID = *ss.ThreadID
	} else if req.Instructions, err = s.Config.Instructions(agents.Planner, s.direction(ctx, courseID)); err != nil {
		return err
	}
	res, err := s.Agent.Turn(ctx, req)
	if res.ThreadID != "" && ss.ThreadID == nil {
		if _, err := s.DB.Exec(ctx, `update agent_sessions set thread_id = $2 where id = $1`, ss.ID, res.ThreadID); err != nil {
			return err
		}
	}
	if err != nil {
		return agentFailure(err)
	}
	if err := agents.ParseResult(kind, res.Text, into); err != nil {
		return agentFailure(err)
	}
	return nil
}
