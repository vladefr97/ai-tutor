// Package generation builds a course version from a confirmed plan: the
// Generator writes it in a clean workspace, the backend checks its structure,
// the Work reviewer approves the exact files, and only then the version is
// uploaded to storage and published.
package generation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vladefr97/tutor-app/backend/internal/agents"
	"github.com/vladefr97/tutor-app/backend/internal/course"
	"github.com/vladefr97/tutor-app/backend/internal/ops"
	"github.com/vladefr97/tutor-app/contracts"
)

const Kind = "generation"

// Stages shown while the course is prepared.
const (
	StageGenerating = "generating"
	StageReviewing  = "reviewing_content"
	StageUploading  = "uploading"
)

// Store is the object storage of course versions.
type Store interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
}

type Service struct {
	DB     *pgxpool.Pool
	Agent  agents.Agent
	Config agents.Config
	Ops    *ops.Runner
	Store  Store
	// MaxStructureFixes bounds the turns spent fixing invalid files; content
	// review cycles are not limited.
	MaxStructureFixes int
	// TemplatesDir holds templates/practice/<direction>, the technical base of
	// the learner repository; copied to input/template for Python and Git.
	TemplatesDir string

	cacheMu sync.Mutex
	cache   map[string]*Version
}

type Input struct {
	PlanVersionID string `json:"planVersionId"`
}

func (s *Service) Register() { s.Ops.Handle(Kind, s.generate) }

func versionPrefix(courseID, versionID string) string {
	return "courses/" + courseID + "/versions/" + versionID
}

func failure(code, message string, err error) error {
	return &ops.Failure{Code: code, Message: message, Err: err}
}

func agentFailure(err error) error {
	return failure("generation_failed", "Не удалось подготовить курс. Нажмите «Повторить».", err)
}

type plan struct {
	HasPractice bool `json:"hasPractice"`
}

func (s *Service) generate(ctx context.Context, op ops.Op) (_ *ops.Next, err error) {
	var in Input
	if err := json.Unmarshal(op.Input, &in); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			s.DB.Exec(context.Background(), `update courses set status = 'generation_failed' where id = $1 and status = 'generating'`, op.CourseID)
		}
	}()
	var direction string
	var planRaw, profile json.RawMessage
	err = s.DB.QueryRow(ctx, `update courses c set status = 'generating'
		from plan_versions v
		where c.id = $1 and v.id = $2 and v.course_id = c.id and c.confirmed_plan_version_id = v.id
		  and c.status in ('generating', 'generation_failed')
		returning c.direction, v.plan, coalesce(c.profile, '{}')`, op.CourseID, in.PlanVersionID).Scan(&direction, &planRaw, &profile)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, failure("not_confirmed", "Курс можно готовить только по подтверждённому плану.", err)
	}
	if err != nil {
		return nil, err
	}
	var p plan
	json.Unmarshal(planRaw, &p)

	g, err := s.newGeneratorSession(ctx, op.CourseID, direction, planRaw, profile)
	if err != nil {
		return nil, err
	}
	if err := s.Ops.SetStage(ctx, op.ID, StageGenerating); err != nil {
		return nil, err
	}
	if err := g.turn(ctx, "[Сгенерируй курс] Входные данные — в /workspace/input, результат запиши в /workspace/out по схемам из input/."); err != nil {
		return nil, err
	}
	out := filepath.Join(g.dir, "workspace", "out")
	for {
		for fixes := 0; ; fixes++ {
			problems := Check(out, direction, p.HasPractice)
			if len(problems) == 0 {
				break
			}
			if fixes >= s.MaxStructureFixes {
				return nil, failure("generation_invalid", "Не удалось собрать корректный курс. Нажмите «Повторить».",
					errors.New(strings.Join(problems, "; ")))
			}
			if err := g.turn(ctx, "[Проверка структуры] Исправь ошибки в /workspace/out:\n- "+strings.Join(problems, "\n- ")); err != nil {
				return nil, err
			}
		}
		hash, err := hashDir(out)
		if err != nil {
			return nil, err
		}
		if err := s.Ops.SetStage(ctx, op.ID, StageReviewing); err != nil {
			return nil, err
		}
		rv, err := s.reviewContent(ctx, op.CourseID, direction, planRaw, profile, out)
		if err != nil {
			return nil, err
		}
		if !rv.Approved {
			if err := s.Ops.SetStage(ctx, op.ID, StageGenerating); err != nil {
				return nil, err
			}
			var b strings.Builder
			b.WriteString("[Ревьюер работ] Замечания к версии курса:\n")
			for _, i := range rv.Issues {
				fmt.Fprintf(&b, "- %s: %s\n", i.ItemID, i.Comment)
			}
			b.WriteString("Исправь файлы в /workspace/out.")
			if err := g.turn(ctx, b.String()); err != nil {
				return nil, err
			}
			continue
		}
		// The approval covers exactly the files the reviewer read.
		if now, err := hashDir(out); err != nil || now != hash {
			if err != nil {
				return nil, err
			}
			continue
		}
		if err := s.Ops.SetStage(ctx, op.ID, StageUploading); err != nil {
			return nil, err
		}
		return nil, s.publish(ctx, op.CourseID, in.PlanVersionID, hash, out)
	}
}

type generatorSession struct {
	s                   *Service
	id, dir, model      string
	thread, instruction string
}

func (s *Service) newGeneratorSession(ctx context.Context, courseID, direction string, plan, profile json.RawMessage) (*generatorSession, error) {
	var n int
	if err := s.DB.QueryRow(ctx, `select count(*) + 1 from agent_sessions where course_id = $1 and role = $2`, courseID, agents.Generator).Scan(&n); err != nil {
		return nil, err
	}
	g := &generatorSession{s: s, dir: s.Config.SessionDir(courseID, agents.Generator, n), model: s.Config.Models[agents.Generator]}
	if err := s.DB.QueryRow(ctx, `insert into agent_sessions (course_id, role, dir, model) values ($1, $2, $3, $4) returning id`,
		courseID, agents.Generator, g.dir, g.model).Scan(&g.id); err != nil {
		return nil, err
	}
	// Clean workspace with only the approved package: no interview history.
	input := filepath.Join(g.dir, "workspace", "input")
	if err := os.RemoveAll(filepath.Join(g.dir, "workspace")); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(input, 0o777); err != nil {
		return nil, err
	}
	base, err := os.ReadFile(filepath.Join(s.Config.InstructionsDir, "directions", direction, "base.md"))
	if err != nil {
		return nil, err
	}
	pub, _ := contracts.FS.ReadFile("course.schema.json")
	priv, _ := contracts.FS.ReadFile("course-private.schema.json")
	for name, data := range map[string][]byte{"plan.json": plan, "profile.json": profile, "base.md": base,
		"course.schema.json": pub, "course-private.schema.json": priv} {
		if err := os.WriteFile(filepath.Join(input, name), data, 0o666); err != nil {
			return nil, err
		}
	}
	if direction != "pm" && s.TemplatesDir != "" {
		if err := os.CopyFS(filepath.Join(input, "template"), os.DirFS(filepath.Join(s.TemplatesDir, "practice", direction))); err != nil {
			return nil, err
		}
	}
	os.MkdirAll(filepath.Join(g.dir, "workspace", "out"), 0o777)
	os.Chmod(filepath.Join(g.dir, "workspace", "out"), 0o777)
	g.instruction, err = s.Config.Instructions(agents.Generator, "")
	return g, err
}

func (g *generatorSession) turn(ctx context.Context, input string) error {
	req := agents.TurnRequest{Role: agents.Generator, SessionDir: g.dir, Model: g.model, Input: input, ThreadID: g.thread}
	if g.thread == "" {
		req.Instructions = g.instruction
	}
	res, err := g.s.Agent.Turn(ctx, req)
	if res.ThreadID != "" && g.thread == "" {
		g.thread = res.ThreadID
		g.s.DB.Exec(ctx, `update agent_sessions set thread_id = $2 where id = $1`, g.id, res.ThreadID)
	}
	if err != nil {
		return agentFailure(err)
	}
	return nil
}

type contentReview struct {
	Approved bool `json:"approved"`
	Issues   []struct {
		ItemID  string `json:"itemId"`
		Comment string `json:"comment"`
	} `json:"issues"`
}

// reviewContent runs the Work reviewer in a fresh session with the version
// mounted read-only.
func (s *Service) reviewContent(ctx context.Context, courseID, direction string, plan, profile json.RawMessage, out string) (contentReview, error) {
	var rv contentReview
	var n int
	if err := s.DB.QueryRow(ctx, `select count(*) + 1 from agent_sessions where course_id = $1 and role = $2`, courseID, agents.WorkReviewer).Scan(&n); err != nil {
		return rv, err
	}
	dir := s.Config.SessionDir(courseID, agents.WorkReviewer, n)
	model := s.Config.Models[agents.WorkReviewer]
	var sessionID string
	if err := s.DB.QueryRow(ctx, `insert into agent_sessions (course_id, role, dir, model) values ($1, $2, $3, $4) returning id`,
		courseID, agents.WorkReviewer, dir, model).Scan(&sessionID); err != nil {
		return rv, err
	}
	instructions, err := s.Config.Instructions(agents.WorkReviewer, direction)
	if err != nil {
		return rv, err
	}
	input, _ := json.Marshal(map[string]any{"mode": "content_review", "exam": false, "direction": direction, "plan": plan,
		"profile": profile, "files": "/workspace/out"})
	res, err := s.Agent.Turn(ctx, agents.TurnRequest{Role: agents.WorkReviewer, SessionDir: dir, Model: model,
		Instructions: instructions, Input: string(input), OutputSchema: agents.OutputSchema(agents.KindContentReview),
		Mounts: []agents.Mount{{HostPath: out, Target: "/workspace/out", ReadOnly: true}}})
	if res.ThreadID != "" {
		s.DB.Exec(ctx, `update agent_sessions set thread_id = $2 where id = $1`, sessionID, res.ThreadID)
	}
	if err != nil {
		return rv, failure("review_failed", "Не удалось проверить курс. Нажмите «Повторить».", err)
	}
	if err := agents.ParseResult(agents.KindContentReview, res.Text, &rv); err != nil {
		return rv, failure("review_failed", "Не удалось проверить курс. Нажмите «Повторить».", err)
	}
	return rv, nil
}

// publish uploads the approved files and only then points the course at the version.
func (s *Service) publish(ctx context.Context, courseID, planVersionID, hash, out string) error {
	var versionID string
	if err := s.DB.QueryRow(ctx, `insert into course_versions (course_id, plan_version_id, content_hash) values ($1, $2, $3) returning id`,
		courseID, planVersionID, hash).Scan(&versionID); err != nil {
		return err
	}
	prefix := versionPrefix(courseID, versionID)
	err := filepath.WalkDir(out, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(out, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return s.Store.Put(ctx, prefix+"/"+filepath.ToSlash(rel), data)
	})
	if err != nil {
		return failure("upload_failed", "Не удалось сохранить курс. Нажмите «Повторить».", err)
	}
	if now, err := hashDir(out); err != nil || now != hash {
		return failure("upload_failed", "Версия курса изменилась при сохранении. Нажмите «Повторить».", err)
	}
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `update course_versions set status = 'ready' where id = $1`, versionID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update courses set content_version_id = $2, status = 'ready' where id = $1 and status = 'generating'`, courseID, versionID)
		if err == nil && tag.RowsAffected() != 1 {
			err = errors.New("course left the generating state before publication")
		}
		return err
	})
}

// hashDir fingerprints every file path and content under dir.
func hashDir(dir string) (string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, path)
		}
		return err
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// PublicCourse returns the learner-visible course.json of a ready version.
func (s *Service) PublicCourse(ctx context.Context, courseID, versionID string) ([]byte, error) {
	return s.Store.Get(ctx, versionPrefix(courseID, versionID)+"/public/course.json")
}

// Version is a parsed published version, both parts. Versions are immutable,
// so they are cached in memory.
type Version struct {
	Public  course.Public
	Private course.Private
}

func (s *Service) Version(ctx context.Context, courseID, versionID string) (*Version, error) {
	s.cacheMu.Lock()
	v, ok := s.cache[versionID]
	s.cacheMu.Unlock()
	if ok {
		return v, nil
	}
	pub, err := s.PublicCourse(ctx, courseID, versionID)
	if err != nil {
		return nil, err
	}
	priv, err := s.Store.Get(ctx, versionPrefix(courseID, versionID)+"/private/private.json")
	if err != nil {
		return nil, err
	}
	v = &Version{}
	if err := json.Unmarshal(pub, &v.Public); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(priv, &v.Private); err != nil {
		return nil, err
	}
	s.cacheMu.Lock()
	if s.cache == nil {
		s.cache = map[string]*Version{}
	}
	s.cache[versionID] = v
	s.cacheMu.Unlock()
	return v, nil
}
