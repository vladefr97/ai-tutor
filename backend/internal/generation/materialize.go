package generation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/vladefr97/tutor-app/backend/internal/course"
)

// Materialize rebuilds the Learning Mentor's read-only folder from the stored
// version: course.json and one Markdown file per page. Only the public part is
// downloaded; answers, hints, explanations, criteria and checks never reach it.
func (s *Service) Materialize(ctx context.Context, versionID, dir string) error {
	var courseID string
	if err := s.DB.QueryRow(ctx, `select course_id from course_versions where id = $1 and status = 'ready'`, versionID).Scan(&courseID); err != nil {
		return err
	}
	raw, err := s.PublicCourse(ctx, courseID, versionID)
	if err != nil {
		return err
	}
	var pub course.Public
	if err := json.Unmarshal(raw, &pub); err != nil {
		return err
	}
	tmp := dir + ".tmp"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "pages"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, "course.json"), raw, 0o644); err != nil {
		return err
	}
	for _, m := range pub.Modules {
		for _, p := range m.Pages {
			if err := os.WriteFile(filepath.Join(tmp, "pages", p.ID+".md"), []byte(renderPage(p)), 0o644); err != nil {
				return err
			}
		}
	}
	os.RemoveAll(dir)
	return os.Rename(tmp, dir)
}
