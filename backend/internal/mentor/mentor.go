// Package mentor runs the Learning Mentor chat on the course page: one Codex
// session per course, course materials mounted read-only, fresh progress in
// every turn.
package mentor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vladefr97/tutor-app/backend/internal/agents"
	"github.com/vladefr97/tutor-app/backend/internal/generation"
	"github.com/vladefr97/tutor-app/backend/internal/ops"
)

const Kind = "tutor_chat"

var ErrNotReady = errors.New("course is not ready")

type Service struct {
	DB     *pgxpool.Pool
	Agent  agents.Agent
	Config agents.Config
	Ops    *ops.Runner
	Gen    *generation.Service
}

func (s *Service) Register() { s.Ops.Handle(Kind, s.reply) }

type input struct {
	Text   string `json:"text"`
	PageID string `json:"pageId"`
}

type Message struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
}

// Post stores the learner's message and queues the mentor's reply.
func (s *Service) Post(ctx context.Context, courseID, text, pageID string) (ops.Op, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return ops.Op{}, err
	}
	defer tx.Rollback(ctx)
	var version *string
	if err := tx.QueryRow(ctx, `select content_version_id from courses where id = $1 for update`, courseID).Scan(&version); err != nil {
		return ops.Op{}, err
	}
	if version == nil {
		return ops.Op{}, ErrNotReady
	}
	op, err := s.Ops.Start(ctx, tx, courseID, Kind, input{Text: text, PageID: pageID})
	if err != nil {
		return op, err
	}
	if _, err := tx.Exec(ctx, `insert into chat_messages (course_id, author, text, page_id) values ($1, 'learner', $2, $3)`,
		courseID, text, pageID); err != nil {
		return op, err
	}
	if err := tx.Commit(ctx); err != nil {
		return op, err
	}
	s.Ops.Launch(op)
	return op, nil
}

func (s *Service) History(ctx context.Context, courseID string) ([]Message, error) {
	rows, err := s.DB.Query(ctx, `select id, author, text, created_at from chat_messages where course_id = $1 order by created_at, id`, courseID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Message])
	if list == nil {
		list = []Message{}
	}
	return list, err
}

type progressItem struct {
	ItemID   string  `json:"itemId"`
	Status   string  `json:"status"`
	Attempts int     `json:"attempts"`
	Feedback *string `json:"feedback,omitempty"`
}

// progress is the latest attempt status and attempt count per item.
func (s *Service) progress(ctx context.Context, courseID, versionID string) ([]progressItem, error) {
	rows, err := s.DB.Query(ctx, `select distinct on (item_id) item_id, status,
		count(*) over (partition by item_id)::int, feedback
		from attempts where course_id = $1 and content_version_id = $2
		order by item_id, created_at desc`, courseID, versionID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[progressItem])
	if list == nil {
		list = []progressItem{}
	}
	return list, err
}

func (s *Service) reply(ctx context.Context, op ops.Op) (*ops.Next, error) {
	var in input
	if err := json.Unmarshal(op.Input, &in); err != nil {
		return nil, err
	}
	var direction string
	var version *string
	var profile json.RawMessage
	if err := s.DB.QueryRow(ctx, `select direction, content_version_id, coalesce(profile, '{}') from courses where id = $1`,
		op.CourseID).Scan(&direction, &version, &profile); err != nil {
		return nil, err
	}
	if version == nil {
		return nil, ErrNotReady
	}
	// The folder is a disposable copy of the version the site shows.
	folder := filepath.Join(s.Config.SessionsDir, op.CourseID, "materials", *version)
	if _, err := os.Stat(filepath.Join(folder, "course.json")); err != nil {
		if err := s.Gen.Materialize(ctx, *version, folder); err != nil {
			return nil, err
		}
	}
	progress, err := s.progress(ctx, op.CourseID, *version)
	if err != nil {
		return nil, err
	}
	sessionID, dir, model, thread, err := s.session(ctx, op.CourseID)
	if err != nil {
		return nil, err
	}
	context, _ := json.Marshal(map[string]any{"pageId": in.PageID, "profile": profile, "progress": progress, "exam": false})
	req := agents.TurnRequest{Role: agents.LearningMentor, SessionDir: dir, Model: model, ThreadID: thread,
		Input:  "[Контекст]\n" + string(context) + "\n\n" + in.Text,
		Mounts: []agents.Mount{{HostPath: folder, Target: "/course", ReadOnly: true}}}
	if thread == "" {
		if req.Instructions, err = s.Config.Instructions(agents.LearningMentor, direction); err != nil {
			return nil, err
		}
	}
	res, err := s.Agent.Turn(ctx, req)
	if res.ThreadID != "" && thread == "" {
		s.DB.Exec(ctx, `update agent_sessions set thread_id = $2 where id = $1`, sessionID, res.ThreadID)
	}
	if err != nil {
		code, msg := "agent_failed", "Наставник не ответил. Нажмите «Повторить»."
		if errors.Is(err, agents.ErrTimeout) {
			code, msg = "agent_timeout", "Наставник не ответил вовремя. Нажмите «Повторить»."
		}
		return nil, &ops.Failure{Code: code, Message: msg, Err: err}
	}
	_, err = s.DB.Exec(ctx, `insert into chat_messages (course_id, author, text, page_id) values ($1, 'mentor', $2, $3)`,
		op.CourseID, res.Text, in.PageID)
	return nil, err
}

// session returns the course's Learning Mentor session, creating it on first use.
func (s *Service) session(ctx context.Context, courseID string) (id, dir, model, thread string, err error) {
	var t *string
	err = s.DB.QueryRow(ctx, `select id, dir, model, thread_id from agent_sessions where course_id = $1 and role = $2
		order by created_at desc limit 1`, courseID, agents.LearningMentor).Scan(&id, &dir, &model, &t)
	if err == nil {
		if t != nil {
			thread = *t
		}
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return
	}
	dir = s.Config.SessionDir(courseID, agents.LearningMentor, 1)
	model = s.Config.Models[agents.LearningMentor]
	err = s.DB.QueryRow(ctx, `insert into agent_sessions (course_id, role, dir, model) values ($1, $2, $3, $4) returning id`,
		courseID, agents.LearningMentor, dir, model).Scan(&id)
	return
}
