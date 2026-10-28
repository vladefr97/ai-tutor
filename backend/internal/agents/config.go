package agents

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config locates prompts and per-session directories and picks models per role.
type Config struct {
	InstructionsDir string // agent-config/: roles/<role>.md, directions/<dir>/base.md
	SessionsDir     string // host path for session directories
	Models          map[Role]string
}

// Instructions reads the role prompt and, for direction roles, the expert base.
// Files are read on every session start, so edits apply to new sessions.
func (c Config) Instructions(role Role, direction string) (string, error) {
	parts := []string{filepath.Join(c.InstructionsDir, "roles", string(role)+".md")}
	if direction != "" {
		parts = append(parts, filepath.Join(c.InstructionsDir, "directions", direction, "base.md"))
	}
	var b strings.Builder
	for _, p := range parts {
		raw, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		b.Write(raw)
		b.WriteString("\n\n")
	}
	return b.String(), nil
}

func (c Config) SessionDir(courseID string, role Role, n int) string {
	return filepath.Join(c.SessionsDir, courseID, string(role)+"-"+strconv.Itoa(n))
}
