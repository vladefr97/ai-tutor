// Command ghcheck verifies GitHub App settings from the environment (.env):
// App JWT → GET /app, installation token → GET /orgs/{org}. Prints no secrets.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/vladefr97/tutor-app/backend/internal/github"
)

func main() {
	for _, k := range []string{"GITHUB_ORG", "GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY_PATH"} {
		if os.Getenv(k) == "" {
			fmt.Fprintln(os.Stderr, k, "is not set")
			os.Exit(2)
		}
	}
	key, err := github.LoadKey(os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "private key:", err)
		os.Exit(1)
	}
	c := &github.Client{API: "https://api.github.com", AppID: os.Getenv("GITHUB_APP_ID"), Key: key, Org: os.Getenv("GITHUB_ORG")}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	slug, err := c.Check(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Printf("OK: App %q installed in %s\n", slug, os.Getenv("GITHUB_ORG"))
}
