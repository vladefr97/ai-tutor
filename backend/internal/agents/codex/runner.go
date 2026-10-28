// Package codex runs agent turns in Codex app-server containers. Each turn
// starts a fresh container with the session's CODEX_HOME and workspace bind
// mounted, so threads survive backend restarts and sessions never share state.
package codex

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/vladefr97/tutor-app/backend/internal/agents"
)

type Runner struct {
	Image       string        // e.g. tutor-codex:0.163.0
	APIKey      string        // OpenAI API key; sent over stdin, never on the command line
	TurnTimeout time.Duration // whole turn incl. container start
	Docker      string        // docker binary, default "docker"
}

type rpcMsg struct {
	ID     *int64          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type conn struct {
	in     io.Writer
	events chan rpcMsg
	seq    int64
}

func (c *conn) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = c.in.Write(append(b, '\n'))
	return err
}

// call sends a request and waits for its response; notifications that arrive
// meanwhile are handed to onEvent.
func (c *conn) call(ctx context.Context, method string, params any, onEvent func(rpcMsg) error) (json.RawMessage, error) {
	c.seq++
	id := c.seq
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case m, ok := <-c.events:
			if !ok {
				return nil, errors.New("app-server exited")
			}
			if m.Method == "" && m.ID != nil && *m.ID == id {
				if m.Error != nil {
					return nil, fmt.Errorf("%s: %s", method, m.Error.Message)
				}
				return m.Result, nil
			}
			if err := onEvent(m); err != nil {
				return nil, err
			}
		}
	}
}

func randomName() string {
	b := make([]byte, 6)
	rand.Read(b)
	return "tutor-agent-" + hex.EncodeToString(b)
}

func (r *Runner) Turn(ctx context.Context, req agents.TurnRequest) (agents.TurnResult, error) {
	ctx, cancel := context.WithTimeout(ctx, r.TurnTimeout)
	defer cancel()
	home := filepath.Join(req.SessionDir, "home")
	work := filepath.Join(req.SessionDir, "workspace")
	for _, d := range []string{home, work} {
		if err := os.MkdirAll(d, 0o777); err != nil {
			return agents.TurnResult{}, err
		}
		os.Chmod(d, 0o777) // container user differs from the backend user
	}
	// The key is written by login into CODEX_HOME; remove it after every turn.
	defer os.Remove(filepath.Join(home, "auth.json"))

	name := randomName()
	docker := r.Docker
	if docker == "" {
		docker = "docker"
	}
	args := []string{"run", "--rm", "-i", "--name", name,
		"--memory", "1g", "--pids-limit", "512", "--security-opt", "no-new-privileges",
		"-v", home + ":/codex", "-v", work + ":/workspace", "-e", "CODEX_HOME=/codex"}
	for _, m := range req.Mounts {
		v := m.HostPath + ":" + m.Target
		if m.ReadOnly {
			v += ":ro"
		}
		args = append(args, "-v", v)
	}
	args = append(args, r.Image)
	cmd := exec.Command(docker, args...)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return agents.TurnResult{}, err
	}
	defer func() {
		exec.Command(docker, "rm", "-f", name).Run()
		stdin.Close()
		cmd.Wait()
	}()
	c := &conn{in: stdin, events: make(chan rpcMsg, 64)}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			var m rpcMsg
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				c.events <- m
			}
		}
		close(c.events)
	}()

	res, err := r.converse(ctx, c, req)
	if errors.Is(err, context.DeadlineExceeded) {
		return res, fmt.Errorf("%w after %s", agents.ErrTimeout, r.TurnTimeout)
	}
	if err != nil && stderr.Len() > 0 {
		err = fmt.Errorf("%w (app-server: %s)", err, stderr.String())
	}
	return res, err
}

func (r *Runner) converse(ctx context.Context, c *conn, req agents.TurnRequest) (agents.TurnResult, error) {
	var answer string
	var turnID string
	var done bool
	var turnErr error
	onEvent := func(m rpcMsg) error {
		if m.Method != "" && m.ID != nil {
			// Server-initiated request (approval, user input): agents run unattended.
			return c.send(map[string]any{"id": *m.ID, "error": map[string]any{"code": -32601, "message": "not supported"}})
		}
		switch m.Method {
		case "item/completed":
			var p struct {
				TurnID string `json:"turnId"`
				Item   struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"item"`
			}
			json.Unmarshal(m.Params, &p)
			if p.Item.Type == "agentMessage" && (turnID == "" || p.TurnID == turnID) {
				answer = p.Item.Text // the final message is the last one
			}
		case "turn/completed":
			var p struct {
				Turn struct {
					ID     string `json:"id"`
					Status string `json:"status"`
					Error  *struct {
						Message string `json:"message"`
					} `json:"error"`
				} `json:"turn"`
			}
			json.Unmarshal(m.Params, &p)
			if turnID != "" && p.Turn.ID != turnID {
				return nil
			}
			done = true
			if p.Turn.Status != "completed" {
				msg := p.Turn.Status
				if p.Turn.Error != nil {
					msg += ": " + p.Turn.Error.Message
				}
				turnErr = errors.New("turn " + msg)
			}
		}
		return nil
	}
	ignore := func(m rpcMsg) error { return onEvent(m) }

	if _, err := c.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "tutor_backend", "version": "0.1.0"}}, ignore); err != nil {
		return agents.TurnResult{}, err
	}
	if err := c.send(map[string]any{"method": "initialized"}); err != nil {
		return agents.TurnResult{}, err
	}
	if _, err := c.call(ctx, "account/login/start", map[string]string{"type": "apiKey", "apiKey": r.APIKey}, ignore); err != nil {
		return agents.TurnResult{}, err
	}
	thread := map[string]any{"cwd": "/workspace", "approvalPolicy": "never", "sandbox": "danger-full-access", "model": req.Model}
	method := "thread/start"
	if req.ThreadID != "" {
		method = "thread/resume"
		thread["threadId"] = req.ThreadID
	} else {
		thread["developerInstructions"] = req.Instructions
	}
	raw, err := c.call(ctx, method, thread, ignore)
	if err != nil {
		return agents.TurnResult{}, err
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &started); err != nil || started.Thread.ID == "" {
		return agents.TurnResult{}, fmt.Errorf("%s: no thread id", method)
	}
	result := agents.TurnResult{ThreadID: started.Thread.ID}

	turn := map[string]any{"threadId": result.ThreadID, "input": []map[string]string{{"type": "text", "text": req.Input}}, "model": req.Model}
	if req.OutputSchema != nil {
		turn["outputSchema"] = req.OutputSchema
	}
	raw, err = c.call(ctx, "turn/start", turn, onEvent)
	if err != nil {
		return result, err
	}
	var ts struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	json.Unmarshal(raw, &ts)
	turnID = ts.Turn.ID
	for !done {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case m, ok := <-c.events:
			if !ok {
				return result, errors.New("app-server exited before the turn completed")
			}
			if err := onEvent(m); err != nil {
				return result, err
			}
		}
	}
	if turnErr != nil {
		return result, turnErr
	}
	if answer == "" {
		return result, errors.New("turn completed without an agent message")
	}
	result.Text = answer
	return result, nil
}

// limitedBuffer keeps the last 4 KiB of stderr for error messages.
type limitedBuffer struct{ b []byte }

func (l *limitedBuffer) Write(p []byte) (int, error) {
	l.b = append(l.b, p...)
	if len(l.b) > 4096 {
		l.b = l.b[len(l.b)-4096:]
	}
	return len(p), nil
}
func (l *limitedBuffer) Len() int       { return len(l.b) }
func (l *limitedBuffer) String() string { return string(l.b) }
