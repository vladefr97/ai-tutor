// Command server runs the ai-tutor backend. `server admin EMAIL` grants the
// teacher flag to an account and exits.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vladefr97/tutor-app/backend/internal/agents"
	"github.com/vladefr97/tutor-app/backend/internal/agents/codex"
	"github.com/vladefr97/tutor-app/backend/internal/agents/fake"
	"github.com/vladefr97/tutor-app/backend/internal/auth"
	"github.com/vladefr97/tutor-app/backend/internal/db"
	"github.com/vladefr97/tutor-app/backend/internal/generation"
	"github.com/vladefr97/tutor-app/backend/internal/httpapi"
	"github.com/vladefr97/tutor-app/backend/internal/mailer"
	"github.com/vladefr97/tutor-app/backend/internal/ops"
	"github.com/vladefr97/tutor-app/backend/internal/planning"
	"github.com/vladefr97/tutor-app/backend/internal/storage"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "%s is required\n", key)
		os.Exit(2)
	}
	return v
}

func duration(key, def string) time.Duration {
	d, err := time.ParseDuration(env(key, def))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", key, err)
		os.Exit(2)
	}
	return d
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, mustEnv("DATABASE_URL"), "")
	if err != nil {
		slog.Error("database", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	authSvc := &auth.Service{
		DB:         pool,
		Mailer:     mailer.SMTP{Addr: env("SMTP_ADDR", "localhost:1025"), From: env("MAIL_FROM", "tutor@localhost")},
		Now:        time.Now,
		CodeTTL:    duration("AUTH_CODE_TTL", "10m"),
		SessionTTL: duration("SESSION_TTL", "720h"),
		Pepper:     mustEnv("AUTH_PEPPER"),
	}

	if len(os.Args) == 3 && os.Args[1] == "admin" {
		if err := authSvc.EnsureTeacher(ctx, os.Args[2]); err != nil {
			slog.Error("admin", "err", err)
			os.Exit(1)
		}
		fmt.Println("teacher flag set for", auth.NormalizeEmail(os.Args[2]))
		return
	}
	if admin := os.Getenv("ADMIN_EMAIL"); admin != "" {
		if err := authSvc.EnsureTeacher(ctx, admin); err != nil {
			slog.Error("admin account", "err", err)
			os.Exit(1)
		}
	}

	var agent agents.Agent = &codex.Runner{Image: env("CODEX_IMAGE", "tutor-codex:0.163.0"),
		APIKey: os.Getenv("OPENAI_API_KEY"), TurnTimeout: duration("AGENT_TURN_TIMEOUT", "180s")}
	if os.Getenv("AGENT_FAKE") == "1" {
		slog.Warn("AGENT_FAKE=1: scripted agents, no model calls")
		agent = fake.Agent{}
	} else if os.Getenv("OPENAI_API_KEY") == "" {
		slog.Error("OPENAI_API_KEY is required (or AGENT_FAKE=1 for scripted agents)")
		os.Exit(2)
	}
	agentCfg := agents.Config{
		InstructionsDir: env("AGENT_CONFIG_DIR", "agent-config"),
		SessionsDir:     mustEnv("AGENT_SESSIONS_DIR"),
		Models: map[agents.Role]string{
			agents.Planner:        env("MODEL_PLANNER", "gpt-6-astra"),
			agents.PlanReviewer:   env("MODEL_PLAN_REVIEWER", "gpt-6-astra"),
			agents.Generator:      env("MODEL_GENERATOR", "gpt-6.1-sol"),
			agents.LearningMentor: env("MODEL_LEARNING_MENTOR", "gpt-6.1-sol"),
			agents.WorkReviewer:   env("MODEL_WORK_REVIEWER", "gpt-6-astra"),
		},
	}
	runner := &ops.Runner{DB: pool, Timeout: duration("OPERATION_TIMEOUT", "60m")}
	if err := runner.FailInterrupted(ctx); err != nil {
		slog.Error("operations", "err", err)
		os.Exit(1)
	}
	planningSvc := &planning.Service{DB: pool, Agent: agent, Config: agentCfg, Ops: runner}
	planningSvc.Register()
	store, err := storage.New(ctx, env("S3_ENDPOINT", "localhost:7070"), env("S3_ACCESS_KEY", "tutor"),
		env("S3_SECRET_KEY", "tutor-secret"), env("S3_BUCKET", "courses"), env("S3_SECURE", "false") == "true")
	if err != nil {
		slog.Error("storage", "err", err)
		os.Exit(1)
	}
	generationSvc := &generation.Service{DB: pool, Agent: agent, Config: agentCfg, Ops: runner, Store: store, MaxStructureFixes: 3}
	generationSvc.Register()

	srv := &http.Server{
		Addr: env("HTTP_ADDR", ":8080"),
		Handler: httpapi.New(httpapi.Deps{DB: pool, Auth: authSvc, Ops: runner, Planning: planningSvc, Generation: generationSvc,
			CookieSecure: env("COOKIE_SECURE", "false") == "true", WebDir: env("WEB_DIR", "web/dist")}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	slog.Info("listening", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("http", "err", err)
		os.Exit(1)
	}
}
