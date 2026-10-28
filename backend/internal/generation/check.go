package generation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/vladefr97/tutor-app/backend/internal/course"
)

// Check lists the reasons the generator output in out/ cannot be published:
// schema and cross-reference problems, practice that does not match the plan,
// missing official checks and, for Python and Git, missing repository files.
func Check(out, direction string, planHasPractice bool) []string {
	pubRaw, err := os.ReadFile(filepath.Join(out, "public", "course.json"))
	if err != nil {
		return []string{"нет файла public/course.json"}
	}
	privRaw, err := os.ReadFile(filepath.Join(out, "private", "private.json"))
	if err != nil {
		return []string{"нет файла private/private.json"}
	}
	var problems []string
	for _, p := range course.Validate(pubRaw, privRaw) {
		problems = append(problems, p.String())
	}
	if len(problems) > 0 {
		return problems
	}
	var pub course.Public
	var priv course.Private
	json.Unmarshal(pubRaw, &pub)
	json.Unmarshal(privRaw, &priv)
	if pub.Direction != direction {
		problems = append(problems, "direction: курс должен быть направления "+direction)
	}
	if entries, _ := os.ReadDir(filepath.Join(out, "public")); len(entries) != 1 {
		problems = append(problems, "public/: кроме course.json в публичной части ничего не должно быть")
	}
	switch {
	case planHasPractice && len(pub.Practice) == 0:
		problems = append(problems, "practice: план предусматривает практику, а в курсе её нет")
	case !planHasPractice && len(pub.Practice) > 0:
		problems = append(problems, "practice: в плане практики нет, уберите её из курса")
	}
	repoPaths := map[string]string{}
	for _, t := range pub.Practice {
		if t.Kind == "code" {
			repoPaths[t.ID] = t.RepoPath
		}
	}
	for _, v := range pub.Exam.Variants {
		for _, part := range v.Parts {
			if part.Kind == "code" {
				repoPaths[part.ID] = part.RepoPath
			}
		}
	}
	for id, repoPath := range repoPaths {
		checks := filepath.Join(out, "private", filepath.FromSlash(priv.Tasks[id].OfficialChecks))
		tests, _ := filepath.Glob(filepath.Join(checks, "test_*.py"))
		if len(tests) == 0 {
			problems = append(problems, "private/"+priv.Tasks[id].OfficialChecks+": нет официальных тестов test_*.py задания "+id)
		}
		if st, err := os.Stat(filepath.Join(out, "repo", filepath.FromSlash(repoPath))); err != nil || !st.IsDir() {
			problems = append(problems, "repo/"+repoPath+": нет каталога задания "+id+" в учебном репозитории")
		}
	}
	if direction != "pm" {
		if _, err := os.Stat(filepath.Join(out, "repo", "README.md")); err != nil {
			problems = append(problems, "repo/README.md: нет инструкции локального запуска")
		}
	} else if _, err := os.Stat(filepath.Join(out, "repo")); err == nil {
		problems = append(problems, "repo/: для Project Management учебный репозиторий не нужен")
	}
	return problems
}

// renderPage turns a page into Markdown for the Learning Mentor: text blocks
// as is, questions with their options but without any answer data.
func renderPage(p course.Page) string {
	var b strings.Builder
	b.WriteString("# " + p.Title + "\n\n")
	for _, bl := range p.Blocks {
		if !bl.IsQuestion() {
			b.WriteString(bl.Markdown + "\n\n")
			continue
		}
		b.WriteString("**Вопрос (" + bl.ID + "):** " + bl.Prompt + "\n\n")
		for _, o := range bl.Options {
			b.WriteString("- " + o.Text + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}
