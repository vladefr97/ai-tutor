package course_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vladefr97/tutor-app/backend/internal/course"
)

const examples = "../../../contracts/examples/course"

func load(t *testing.T, dir string) ([]byte, []byte) {
	t.Helper()
	pub, err := os.ReadFile(filepath.Join(dir, "course.json"))
	if err != nil {
		t.Fatal(err)
	}
	priv, err := os.ReadFile(filepath.Join(dir, "private.json"))
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func dirs(t *testing.T, kind string) []string {
	t.Helper()
	d, err := filepath.Glob(filepath.Join(examples, kind, "*"))
	if err != nil || len(d) == 0 {
		t.Fatalf("no %s examples: %v", kind, err)
	}
	return d
}

func TestValidExamplesAccepted(t *testing.T) {
	for _, dir := range dirs(t, "valid") {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			pub, priv := load(t, dir)
			if problems := course.Validate(pub, priv); len(problems) > 0 {
				t.Fatalf("valid example rejected: %v", problems)
			}
		})
	}
}

// Each invalid example has expect.txt with the problem code it must be rejected with.
func TestInvalidExamplesRejected(t *testing.T) {
	for _, dir := range dirs(t, "invalid") {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			pub, priv := load(t, dir)
			want, err := os.ReadFile(filepath.Join(dir, "expect.txt"))
			if err != nil {
				t.Fatal(err)
			}
			code := strings.TrimSpace(string(want))
			problems := course.Validate(pub, priv)
			for _, p := range problems {
				if p.Code == code {
					return
				}
			}
			t.Fatalf("want problem %q, got %v", code, problems)
		})
	}
}
