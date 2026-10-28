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

	"github.com/vladefr97/tutor-app/backend/internal/auth"
	"github.com/vladefr97/tutor-app/backend/internal/db"
	"github.com/vladefr97/tutor-app/backend/internal/httpapi"
	"github.com/vladefr97/tutor-app/backend/internal/mailer"
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

	srv := &http.Server{
		Addr: env("HTTP_ADDR", ":8080"),
		Handler: httpapi.New(httpapi.Deps{DB: pool, Auth: authSvc,
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
