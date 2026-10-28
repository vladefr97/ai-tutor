// Package course validates and describes a course version: the public part
// (contracts/course.schema.json) and the private part with answer keys,
// hints, explanations, criteria and official checks
// (contracts/course-private.schema.json).
package course

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/vladefr97/tutor-app/contracts"
)

// Problem is one reason a course version cannot be published.
type Problem struct{ Code, Path, Message string }

func (p Problem) String() string { return p.Code + " " + p.Path + ": " + p.Message }

type Option struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type Block struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Markdown string   `json:"markdown,omitempty"`
	Prompt   string   `json:"prompt,omitempty"`
	Options  []Option `json:"options,omitempty"`
}

func (b Block) IsQuestion() bool { return b.Type != "markdown" }

type Page struct {
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	Blocks []Block `json:"blocks"`
}

type Module struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Pages []Page `json:"pages"`
}

type Task struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Kind         string   `json:"kind"`
	Statement    string   `json:"statement"`
	RelatedPages []string `json:"relatedPages"`
	RepoPath     string   `json:"repoPath,omitempty"`
}

type ExamPart struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Kind      string  `json:"kind"`
	Statement string  `json:"statement,omitempty"`
	Questions []Block `json:"questions,omitempty"`
	RepoPath  string  `json:"repoPath,omitempty"`
}

type ExamVariant struct {
	ID    string     `json:"id"`
	Title string     `json:"title"`
	Parts []ExamPart `json:"parts"`
}

// Public is the learner-visible part of a course version.
type Public struct {
	SchemaVersion int      `json:"schemaVersion"`
	Direction     string   `json:"direction"`
	Title         string   `json:"title"`
	Goal          string   `json:"goal"`
	Modules       []Module `json:"modules"`
	Practice      []Task   `json:"practice"`
	Exam          struct {
		Variants []ExamVariant `json:"variants"`
	} `json:"exam"`
}

type QuestionKey struct {
	Correct     []string `json:"correct,omitempty"`
	Criteria    []string `json:"criteria,omitempty"`
	Hint        string   `json:"hint,omitempty"`
	Explanation string   `json:"explanation,omitempty"`
}

type TaskKey struct {
	Criteria       []string `json:"criteria"`
	OfficialChecks string   `json:"officialChecks,omitempty"`
}

// Private is the service part: never sent to the browser or the Learning Mentor folder.
type Private struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Questions     map[string]QuestionKey `json:"questions"`
	Tasks         map[string]TaskKey     `json:"tasks"`
}

var (
	schemasOnce           sync.Once
	publicSch, privateSch *jsonschema.Schema
)

func schemas() (*jsonschema.Schema, *jsonschema.Schema) {
	schemasOnce.Do(func() {
		c := jsonschema.NewCompiler()
		for _, name := range []string{"course.schema.json", "course-private.schema.json"} {
			raw, err := contracts.FS.ReadFile(name)
			if err != nil {
				panic(err)
			}
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				panic(err)
			}
			if err := c.AddResource(name, doc); err != nil {
				panic(err)
			}
		}
		publicSch = c.MustCompile("course.schema.json")
		privateSch = c.MustCompile("course-private.schema.json")
	})
	return publicSch, privateSch
}

func checkSchema(sch *jsonschema.Schema, raw []byte, path string) []Problem {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return []Problem{{"invalid_json", path, err.Error()}}
	}
	if err := sch.Validate(doc); err != nil {
		return []Problem{{"schema", path, err.Error()}}
	}
	return nil
}

// Validate returns every problem that blocks publishing the version; nil means valid.
func Validate(public, private []byte) []Problem {
	pubSch, privSch := schemas()
	problems := append(checkSchema(pubSch, public, "course.json"), checkSchema(privSch, private, "private.json")...)
	if len(problems) > 0 {
		return problems
	}
	var pub Public
	var priv Private
	if err := json.Unmarshal(public, &pub); err != nil {
		return []Problem{{"invalid_json", "course.json", err.Error()}}
	}
	if err := json.Unmarshal(private, &priv); err != nil {
		return []Problem{{"invalid_json", "private.json", err.Error()}}
	}
	return crossCheck(pub, priv)
}

func crossCheck(pub Public, priv Private) []Problem {
	var out []Problem
	add := func(code, path, format string, args ...any) {
		out = append(out, Problem{code, path, fmt.Sprintf(format, args...)})
	}
	seen := map[string]bool{}
	id := func(path, v string) {
		if seen[v] {
			add("duplicate_id", path, "ID %q уже используется", v)
		}
		seen[v] = true
	}
	usedQ, usedT := map[string]bool{}, map[string]bool{}
	// question checks the answer key; embedded questions also need hint and explanation.
	question := func(path string, q Block, embedded bool) {
		id(path, q.ID)
		usedQ[q.ID] = true
		k, ok := priv.Questions[q.ID]
		if !ok {
			add("missing_key", path, "нет служебной записи вопроса %q", q.ID)
			return
		}
		if q.Type == "short_answer" {
			if len(k.Criteria) == 0 || len(k.Correct) > 0 {
				add("missing_key", path, "короткому ответу нужны criteria и не нужен correct")
			}
		} else {
			opts := map[string]bool{}
			for _, o := range q.Options {
				opts[o.ID] = true
			}
			if len(k.Correct) == 0 {
				add("missing_key", path, "нет правильных вариантов")
			}
			for _, c := range k.Correct {
				if !opts[c] {
					add("unknown_option", path, "правильный вариант %q отсутствует среди вариантов", c)
				}
			}
			if q.Type == "single_choice" && len(k.Correct) > 1 {
				add("unknown_option", path, "у выбора одного ответа ровно один правильный вариант")
			}
		}
		if embedded && (k.Hint == "" || k.Explanation == "") {
			add("missing_help", path, "встроенному вопросу нужны подсказка и разбор")
		}
	}
	// task checks criteria and, for code, official checks.
	task := func(path, tid, kind string) {
		usedT[tid] = true
		k, ok := priv.Tasks[tid]
		if !ok || len(k.Criteria) == 0 {
			add("missing_key", path, "нет критериев зачёта задания %q", tid)
			return
		}
		if (kind == "code") != (k.OfficialChecks != "") {
			add("missing_checks", path, "официальные проверки нужны только и обязательно заданию с кодом")
		}
	}
	practiceKind := map[string]string{"pm": "case", "python": "code", "git": "code"}[pub.Direction]

	pages := map[string]bool{}
	for mi, m := range pub.Modules {
		id(fmt.Sprintf("modules[%d]", mi), m.ID)
		for pi, pg := range m.Pages {
			path := fmt.Sprintf("modules[%d].pages[%d]", mi, pi)
			id(path, pg.ID)
			pages[pg.ID] = true
			for bi, b := range pg.Blocks {
				bpath := fmt.Sprintf("%s.blocks[%d]", path, bi)
				if b.IsQuestion() {
					question(bpath, b, true)
				} else {
					id(bpath, b.ID)
				}
			}
		}
	}
	for ti, t := range pub.Practice {
		path := fmt.Sprintf("practice[%d]", ti)
		id(path, t.ID)
		if t.Kind != practiceKind {
			add("wrong_kind", path, "практика направления %s — %s, а не %s", pub.Direction, practiceKind, t.Kind)
		}
		for _, ref := range t.RelatedPages {
			if !pages[ref] {
				add("unknown_page", path, "ссылка на отсутствующую страницу %q", ref)
			}
		}
		task(path, t.ID, t.Kind)
	}
	for vi, v := range pub.Exam.Variants {
		vpath := fmt.Sprintf("exam.variants[%d]", vi)
		id(vpath, v.ID)
		kinds := map[string]bool{}
		for pi, part := range v.Parts {
			path := fmt.Sprintf("%s.parts[%d]", vpath, pi)
			id(path, part.ID)
			kinds[part.Kind] = true
			if part.Kind == "quiz" {
				for qi, q := range part.Questions {
					question(fmt.Sprintf("%s.questions[%d]", path, qi), q, false)
				}
			} else {
				task(path, part.ID, part.Kind)
			}
		}
		if pub.Direction == "pm" && !(kinds["quiz"] && kinds["case"] && !kinds["code"]) {
			add("exam_parts", vpath, "экзамен Project Management: тестирование знаний и кейс")
		}
		if pub.Direction != "pm" && !(kinds["code"] && !kinds["case"]) {
			add("exam_parts", vpath, "экзамен %s: задания с кодом", pub.Direction)
		}
	}
	for k := range priv.Questions {
		if !usedQ[k] {
			add("orphan_key", "private.questions."+k, "служебная запись без вопроса")
		}
	}
	for k := range priv.Tasks {
		if !usedT[k] {
			add("orphan_key", "private.tasks."+k, "служебная запись без задания")
		}
	}
	return out
}
