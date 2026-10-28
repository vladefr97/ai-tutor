// Package github is a minimal GitHub App client on the standard library:
// App JWT, cached installation token, idempotent repository creation from a
// template, commits, invitations, OAuth user-to-server, check runs, reviews,
// merges and webhook signatures. See docs/implementation-decisions.md, «GitHub».
package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	ErrHeadChanged  = errors.New("github: pull request head changed")     // merge 409
	ErrNotMergeable = errors.New("github: pull request is not mergeable") // merge 405, conflict
)

// APIError is a non-2xx GitHub response.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("github: %d %s", e.Status, e.Message) }

func IsStatus(err error, status int) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == status
}

type Client struct {
	API          string // https://api.github.com
	Web          string // https://github.com (OAuth)
	AppID        string
	Key          *rsa.PrivateKey
	Org          string
	Template     string // template repository in Org
	ClientID     string
	ClientSecret string
	HTTP         *http.Client
	Now          func() time.Time
	// Observe receives every API call; err wraps context.DeadlineExceeded on timeout.
	Observe func(operation string, d time.Duration, err error)

	mu             sync.Mutex
	installationID int64
	token          string
	tokenExpires   time.Time
}

// LoadKey reads the App private key (PKCS#1 as GitHub issues it, or PKCS#8).
func LoadKey(path string) (*rsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("github: no PEM block in key file")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github: key is not RSA")
	}
	return rk, nil
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// JWT authenticates as the App itself (10 minutes max; iat backdated for clock drift).
func (c *Client) JWT() (string, error) {
	enc := base64.RawURLEncoding
	now := c.now().Unix()
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{"iat": now - 60, "exp": now + 540, "iss": c.AppID})
	signing := header + "." + enc.EncodeToString(payload)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.Key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

type auth int

const (
	asApp auth = iota
	asInstallation
)

// installationToken returns a cached token, refreshing it a minute before expiry.
func (c *Client) installationToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.now().Before(c.tokenExpires.Add(-time.Minute)) {
		return c.token, nil
	}
	if c.installationID == 0 {
		var inst struct {
			ID int64 `json:"id"`
		}
		if err := c.call(ctx, "installation", asApp, "", "GET", "/orgs/"+c.Org+"/installation", nil, &inst); err != nil {
			return "", err
		}
		c.installationID = inst.ID
	}
	var tok struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := c.call(ctx, "installation_token", asApp, "", "POST", fmt.Sprintf("/app/installations/%d/access_tokens", c.installationID), nil, &tok); err != nil {
		return "", err
	}
	c.token, c.tokenExpires = tok.Token, tok.ExpiresAt
	return c.token, nil
}

// do calls the API as the installation.
func (c *Client) do(ctx context.Context, op, method, path string, body, out any) error {
	tok, err := c.installationToken(ctx)
	if err != nil {
		return err
	}
	return c.call(ctx, op, asInstallation, tok, method, path, body, out)
}

func (c *Client) call(ctx context.Context, op string, a auth, token, method, path string, body, out any) (err error) {
	start := time.Now()
	defer func() {
		if c.Observe != nil {
			c.Observe(op, time.Since(start), err)
		}
	}()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.API+path, rd)
	if err != nil {
		return err
	}
	if a == asApp {
		if token, err = c.JWT(); err != nil {
			return err
		}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var m struct {
			Message string `json:"message"`
		}
		json.Unmarshal(raw, &m)
		return &APIError{Status: resp.StatusCode, Message: m.Message}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

type Repo struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
}

func (c *Client) repoPath(name string) string { return "/repos/" + c.Org + "/" + name }

// EnsureRepo returns the course repository, creating it from the template
// only if it does not exist. After any failed or lost create response it
// looks the repository up again instead of creating a second one.
func (c *Client) EnsureRepo(ctx context.Context, name, description string) (Repo, bool, error) {
	var r Repo
	err := c.do(ctx, "get_repo", "GET", c.repoPath(name), nil, &r)
	if err == nil {
		return r, false, nil
	}
	if !IsStatus(err, http.StatusNotFound) {
		return r, false, err
	}
	err = c.do(ctx, "generate_repo", "POST", c.repoPath(c.Template)+"/generate",
		map[string]any{"owner": c.Org, "name": name, "description": description, "private": true}, &r)
	if err == nil {
		return r, true, nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status != http.StatusUnprocessableEntity {
		return r, false, err // a definite refusal: nothing was created
	}
	// 422 (already exists) or no response at all: the repository may exist.
	if getErr := c.do(ctx, "get_repo", "GET", c.repoPath(name), nil, &r); getErr == nil {
		return r, false, nil
	}
	return r, false, err
}

// CommitFiles adds one commit with files on top of the branch head.
func (c *Client) CommitFiles(ctx context.Context, repo, branch string, files map[string][]byte, message string) (string, error) {
	base := c.repoPath(repo) + "/git"
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.do(ctx, "get_ref", "GET", base+"/ref/heads/"+branch, nil, &ref); err != nil {
		return "", err
	}
	var head struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := c.do(ctx, "get_commit", "GET", base+"/commits/"+ref.Object.SHA, nil, &head); err != nil {
		return "", err
	}
	var entries []map[string]string
	for path, content := range files {
		var blob struct {
			SHA string `json:"sha"`
		}
		if err := c.do(ctx, "create_blob", "POST", base+"/blobs",
			map[string]string{"content": base64.StdEncoding.EncodeToString(content), "encoding": "base64"}, &blob); err != nil {
			return "", err
		}
		entries = append(entries, map[string]string{"path": path, "mode": "100644", "type": "blob", "sha": blob.SHA})
	}
	var tree, commit struct {
		SHA string `json:"sha"`
	}
	if err := c.do(ctx, "create_tree", "POST", base+"/trees", map[string]any{"base_tree": head.Tree.SHA, "tree": entries}, &tree); err != nil {
		return "", err
	}
	if err := c.do(ctx, "create_commit", "POST", base+"/commits",
		map[string]any{"message": message, "tree": tree.SHA, "parents": []string{ref.Object.SHA}}, &commit); err != nil {
		return "", err
	}
	return commit.SHA, c.do(ctx, "update_ref", "PATCH", base+"/refs/heads/"+branch, map[string]any{"sha": commit.SHA}, nil)
}

type AccessState string

const (
	AccessNone    AccessState = "none"
	AccessInvited AccessState = "invited"
	AccessReady   AccessState = "ready"
)

// Invite gives login push access. An empty URL means the user already has access.
func (c *Client) Invite(ctx context.Context, repo, login string) (string, error) {
	var inv struct {
		HTMLURL string `json:"html_url"`
	}
	err := c.do(ctx, "invite", "PUT", c.repoPath(repo)+"/collaborators/"+login, map[string]string{"permission": "push"}, &inv)
	return inv.HTMLURL, err
}

func (c *Client) Access(ctx context.Context, repo, login string) (AccessState, error) {
	err := c.do(ctx, "get_collaborator", "GET", c.repoPath(repo)+"/collaborators/"+login, nil, nil)
	if err == nil {
		return AccessReady, nil
	}
	if !IsStatus(err, http.StatusNotFound) {
		return AccessNone, err
	}
	var invs []struct {
		Invitee struct {
			Login string `json:"login"`
		} `json:"invitee"`
	}
	if err := c.do(ctx, "list_invitations", "GET", c.repoPath(repo)+"/invitations?per_page=100", nil, &invs); err != nil {
		return AccessNone, err
	}
	for _, i := range invs {
		if strings.EqualFold(i.Invitee.Login, login) {
			return AccessInvited, nil
		}
	}
	return AccessNone, nil
}

type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

func (c *Client) AuthorizeURL(state string) string {
	return c.Web + "/login/oauth/authorize?" + url.Values{"client_id": {c.ClientID}, "state": {state}}.Encode()
}

// ExchangeCode turns an OAuth callback code into the GitHub user; the user
// token is used only for this lookup.
func (c *Client) ExchangeCode(ctx context.Context, code string) (u User, err error) {
	start := time.Now()
	defer func() {
		if c.Observe != nil {
			c.Observe("oauth_exchange", time.Since(start), err)
		}
	}()
	form := url.Values{"client_id": {c.ClientID}, "client_secret": {c.ClientSecret}, "code": {code}}
	req, err := http.NewRequestWithContext(ctx, "POST", c.Web+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return u, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return u, err
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return u, err
	}
	if tok.AccessToken == "" {
		return u, fmt.Errorf("github oauth: %s", tok.Error)
	}
	return u, c.call(ctx, "get_user", asInstallation, tok.AccessToken, "GET", "/user", nil, &u)
}

// VerifySignature checks X-Hub-Signature-256 in constant time.
func VerifySignature(secret string, body []byte, header string) bool {
	got, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	sig, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}

// CreateCheckRun starts an in-progress check run on headSHA; externalID is our attempt ID.
func (c *Client) CreateCheckRun(ctx context.Context, repo, headSHA, name, externalID string) (int64, string, error) {
	var run struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	err := c.do(ctx, "create_check_run", "POST", c.repoPath(repo)+"/check-runs",
		map[string]any{"name": name, "head_sha": headSHA, "status": "in_progress", "external_id": externalID}, &run)
	return run.ID, run.HTMLURL, err
}

// CompleteCheckRun sets the conclusion: success, failure or neutral (technical error).
func (c *Client) CompleteCheckRun(ctx context.Context, repo string, id int64, conclusion, title, summary string) error {
	return c.do(ctx, "complete_check_run", "PATCH", fmt.Sprintf("%s/check-runs/%d", c.repoPath(repo), id),
		map[string]any{"status": "completed", "conclusion": conclusion, "output": map[string]string{"title": title, "summary": summary}}, nil)
}

type PullRequest struct {
	Number    int    `json:"number"`
	State     string `json:"state"`
	Merged    bool   `json:"merged"`
	Mergeable *bool  `json:"mergeable"` // nil while GitHub computes it
	HTMLURL   string `json:"html_url"`
	Head      struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

func (c *Client) PullRequest(ctx context.Context, repo string, n int) (PullRequest, error) {
	var pr PullRequest
	err := c.do(ctx, "get_pull", "GET", fmt.Sprintf("%s/pulls/%d", c.repoPath(repo), n), nil, &pr)
	return pr, err
}

// Review publishes a review on the exact commit; event is APPROVE, REQUEST_CHANGES or COMMENT.
func (c *Client) Review(ctx context.Context, repo string, n int, commitSHA, event, body string) error {
	return c.do(ctx, "review", "POST", fmt.Sprintf("%s/pulls/%d/reviews", c.repoPath(repo), n),
		map[string]string{"commit_id": commitSHA, "event": event, "body": body}, nil)
}

// Merge merges only if the head is still sha.
func (c *Client) Merge(ctx context.Context, repo string, n int, sha string) error {
	err := c.do(ctx, "merge", "PUT", fmt.Sprintf("%s/pulls/%d/merge", c.repoPath(repo), n), map[string]string{"sha": sha}, nil)
	switch {
	case IsStatus(err, http.StatusConflict):
		return fmt.Errorf("%w: %v", ErrHeadChanged, err)
	case IsStatus(err, http.StatusMethodNotAllowed):
		return fmt.Errorf("%w: %v", ErrNotMergeable, err)
	}
	return err
}

// Tarball downloads the repository at sha; the caller closes the body.
func (c *Client) Tarball(ctx context.Context, repo, sha string) (io.ReadCloser, error) {
	tok, err := c.installationToken(ctx)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", c.API+c.repoPath(repo)+"/tarball/"+sha, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.httpClient().Do(req)
	if err == nil && resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		err = &APIError{Status: resp.StatusCode, Message: "tarball"}
	}
	if c.Observe != nil {
		c.Observe("tarball", time.Since(start), err)
	}
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Check verifies the setup: the App JWT is accepted and the installation
// token can read the organization. Returns the App slug.
func (c *Client) Check(ctx context.Context) (string, error) {
	var app struct {
		Slug string `json:"slug"`
	}
	if err := c.call(ctx, "get_app", asApp, "", "GET", "/app", nil, &app); err != nil {
		return "", fmt.Errorf("App JWT: %w", err)
	}
	if err := c.do(ctx, "get_org", "GET", "/orgs/"+c.Org, nil, nil); err != nil {
		return app.Slug, fmt.Errorf("installation in %s: %w", c.Org, err)
	}
	return app.Slug, nil
}
