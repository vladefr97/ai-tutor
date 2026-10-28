package github_test

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vladefr97/tutor-app/backend/internal/github"
)

// fakeGitHub implements the slice of the REST API the client uses.
type fakeGitHub struct {
	t   *testing.T
	pub *rsa.PublicKey
	srv *httptest.Server

	mu           sync.Mutex
	repos        map[string]bool
	generates    int
	dropCreate   bool   // close the connection after creating, before responding
	hideOnce     string // GET of this repo answers 404 once
	tokens       int
	head         string
	commits      map[string]string // sha -> tree
	treeFiles    map[string]string
	collab       map[string]string // login -> none|invited|ready
	mergeStatus  int
	observed     []string
	lastCheckRun map[string]any
	lastReview   map[string]string
}

func newFake(t *testing.T) (*fakeGitHub, *github.Client) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeGitHub{t: t, pub: &key.PublicKey, repos: map[string]bool{"tmpl": true}, head: "c0",
		commits: map[string]string{"c0": "t0"}, collab: map[string]string{}, mergeStatus: 200}
	mux := http.NewServeMux()
	app := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !f.validJWT(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) {
				http.Error(w, `{"message":"bad jwt"}`, 401)
				return
			}
			h(w, r)
		}
	}
	inst := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-") {
				http.Error(w, `{"message":"bad token"}`, 401)
				return
			}
			h(w, r)
		}
	}
	reply := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if v != nil {
			json.NewEncoder(w).Encode(v)
		}
	}
	repo := func(name string) map[string]any {
		return map[string]any{"name": name, "full_name": "org/" + name, "html_url": "https://github.test/org/" + name, "private": true, "default_branch": "main"}
	}
	mux.HandleFunc("GET /app", app(func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]string{"slug": "tutor-app"}) }))
	mux.HandleFunc("GET /orgs/org", inst(func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]string{"login": "org"}) }))
	mux.HandleFunc("GET /orgs/org/installation", app(func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]any{"id": 42}) }))
	mux.HandleFunc("POST /app/installations/42/access_tokens", app(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.tokens++
		n := f.tokens
		f.mu.Unlock()
		reply(w, 201, map[string]any{"token": fmt.Sprintf("tok-%d", n), "expires_at": time.Now().Add(time.Hour)})
	}))
	mux.HandleFunc("GET /repos/org/{name}", inst(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		ok := f.repos[r.PathValue("name")]
		if f.hideOnce == r.PathValue("name") {
			ok, f.hideOnce = false, ""
		}
		f.mu.Unlock()
		if !ok {
			reply(w, 404, map[string]string{"message": "Not Found"})
			return
		}
		reply(w, 200, repo(r.PathValue("name")))
	}))
	mux.HandleFunc("POST /repos/org/tmpl/generate", inst(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name    string
			Private bool
		}
		json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.generates++
		exists := f.repos[in.Name]
		f.repos[in.Name] = true
		drop := f.dropCreate
		f.mu.Unlock()
		if !in.Private {
			t.Error("repository must be private")
		}
		if exists {
			reply(w, 422, map[string]string{"message": "Repository creation failed: name already exists on this account"})
			return
		}
		if drop {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		reply(w, 201, repo(in.Name))
	}))
	mux.HandleFunc("GET /repos/org/{name}/git/ref/heads/main", inst(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		reply(w, 200, map[string]any{"object": map[string]string{"sha": f.head}})
	}))
	mux.HandleFunc("GET /repos/org/{name}/git/commits/{sha}", inst(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		reply(w, 200, map[string]any{"tree": map[string]string{"sha": f.commits[r.PathValue("sha")]}})
	}))
	mux.HandleFunc("POST /repos/org/{name}/git/blobs", inst(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Content, Encoding string }
		json.NewDecoder(r.Body).Decode(&in)
		b, _ := base64.StdEncoding.DecodeString(in.Content)
		reply(w, 201, map[string]string{"sha": "blob:" + string(b)})
	}))
	mux.HandleFunc("POST /repos/org/{name}/git/trees", inst(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			BaseTree string `json:"base_tree"`
			Tree     []map[string]string
		}
		json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		if in.BaseTree != f.commits[f.head] {
			t.Errorf("tree must be based on the head tree, got %q", in.BaseTree)
		}
		f.treeFiles = map[string]string{}
		for _, e := range in.Tree {
			f.treeFiles[e["path"]] = strings.TrimPrefix(e["sha"], "blob:")
		}
		reply(w, 201, map[string]string{"sha": "t1"})
	}))
	mux.HandleFunc("POST /repos/org/{name}/git/commits", inst(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Tree    string
			Parents []string
		}
		json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(in.Parents) != 1 || in.Parents[0] != f.head {
			t.Errorf("commit parent must be the head, got %v", in.Parents)
		}
		f.commits["c1"] = in.Tree
		reply(w, 201, map[string]string{"sha": "c1"})
	}))
	mux.HandleFunc("PATCH /repos/org/{name}/git/refs/heads/main", inst(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ SHA string }
		json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.head = in.SHA
		f.mu.Unlock()
		reply(w, 200, nil)
	}))
	mux.HandleFunc("PUT /repos/org/{name}/collaborators/{login}", inst(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Permission string }
		json.NewDecoder(r.Body).Decode(&in)
		if in.Permission != "push" {
			t.Errorf("permission %q", in.Permission)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		login := r.PathValue("login")
		if f.collab[login] == "ready" {
			reply(w, 204, nil)
			return
		}
		f.collab[login] = "invited"
		reply(w, 201, map[string]string{"html_url": "https://github.test/org/" + r.PathValue("name") + "/invitations"})
	}))
	mux.HandleFunc("GET /repos/org/{name}/collaborators/{login}", inst(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.collab[r.PathValue("login")] == "ready" {
			reply(w, 204, nil)
			return
		}
		reply(w, 404, map[string]string{"message": "Not Found"})
	}))
	mux.HandleFunc("GET /repos/org/{name}/invitations", inst(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var list []any
		for login, s := range f.collab {
			if s == "invited" {
				list = append(list, map[string]any{"invitee": map[string]string{"login": login}})
			}
		}
		reply(w, 200, list)
	}))
	mux.HandleFunc("POST /repos/org/{name}/check-runs", inst(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.lastCheckRun = in
		f.mu.Unlock()
		reply(w, 201, map[string]any{"id": 7, "html_url": "https://github.test/org/r/runs/7"})
	}))
	mux.HandleFunc("PATCH /repos/org/{name}/check-runs/7", inst(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.lastCheckRun = in
		f.mu.Unlock()
		reply(w, 200, nil)
	}))
	mux.HandleFunc("GET /repos/org/{name}/pulls/3", inst(func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"number": 3, "state": "open", "mergeable": true, "head": map[string]string{"sha": "abc"}})
	}))
	mux.HandleFunc("POST /repos/org/{name}/pulls/3/reviews", inst(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.lastReview = in
		f.mu.Unlock()
		reply(w, 200, map[string]any{"id": 1})
	}))
	mux.HandleFunc("PUT /repos/org/{name}/pulls/3/merge", inst(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		st := f.mergeStatus
		f.mu.Unlock()
		reply(w, st, map[string]string{"message": "merge"})
	}))
	mux.HandleFunc("GET /repos/org/{name}/tarball/{sha}", inst(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tar:" + r.PathValue("sha")))
	}))
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("code") != "good" || r.Form.Get("client_secret") != "secret" {
			reply(w, 200, map[string]string{"error": "bad_verification_code"})
			return
		}
		reply(w, 200, map[string]string{"access_token": "user-token"})
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer user-token" {
			reply(w, 401, map[string]string{"message": "bad"})
			return
		}
		reply(w, 200, map[string]any{"id": 99, "login": "anna"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	c := &github.Client{API: f.srv.URL, Web: f.srv.URL, AppID: "123", Key: key, Org: "org", Template: "tmpl",
		ClientID: "cid", ClientSecret: "secret",
		Observe: func(op string, _ time.Duration, err error) {
			f.mu.Lock()
			f.observed = append(f.observed, op)
			f.mu.Unlock()
		}}
	return f, c
}

func (f *fakeGitHub) validJWT(tok string) bool {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(f.pub, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Iss      string
		Iat, Exp int64
	}
	json.Unmarshal(raw, &claims)
	return claims.Iss == "123" && claims.Exp-claims.Iat <= 600 && claims.Exp > time.Now().Unix()
}

var ctx = context.Background()

func TestEnsureRepoCreatesOnceAndFindsExisting(t *testing.T) {
	f, c := newFake(t)
	r, created, err := c.EnsureRepo(ctx, "course-1", "курс")
	if err != nil || !created || !r.Private || r.FullName != "org/course-1" {
		t.Fatalf("create: %+v %v %v", r, created, err)
	}
	r, created, err = c.EnsureRepo(ctx, "course-1", "курс")
	if err != nil || created || r.Name != "course-1" {
		t.Fatalf("repeat: %+v %v %v", r, created, err)
	}
	if f.generates != 1 {
		t.Fatalf("generate calls: %d", f.generates)
	}
}

func TestEnsureRepoAfterLostCreateResponseFindsTheRepo(t *testing.T) {
	f, c := newFake(t)
	f.dropCreate = true
	r, _, err := c.EnsureRepo(ctx, "course-2", "")
	if err != nil || r.Name != "course-2" {
		t.Fatalf("lost response: %+v %v", r, err)
	}
	f.dropCreate = false
	if _, created, _ := c.EnsureRepo(ctx, "course-2", ""); created || f.generates != 1 {
		t.Fatalf("second repository created: generates=%d", f.generates)
	}
}

func TestEnsureRepoTreats422AsExisting(t *testing.T) {
	f, c := newFake(t)
	// Created by a concurrent request: our GET misses it, generate answers 422.
	f.repos["course-3"] = true
	f.hideOnce = "course-3"
	r, created, err := c.EnsureRepo(ctx, "course-3", "")
	if err != nil || created || r.Name != "course-3" {
		t.Fatalf("422 path: %+v %v %v", r, created, err)
	}
}

func TestCommitFilesGoesOnTopOfHead(t *testing.T) {
	f, c := newFake(t)
	sha, err := c.CommitFiles(ctx, "course-1", "main", map[string][]byte{"tasks/a/README.md": []byte("условие")}, "Задания курса")
	if err != nil || sha != "c1" || f.head != "c1" {
		t.Fatalf("commit: %s %v head=%s", sha, err, f.head)
	}
	if f.treeFiles["tasks/a/README.md"] != "условие" {
		t.Fatalf("tree: %+v", f.treeFiles)
	}
}

func TestInviteAndAccessStates(t *testing.T) {
	f, c := newFake(t)
	if st, err := c.Access(ctx, "r", "anna"); err != nil || st != github.AccessNone {
		t.Fatalf("before invite: %s %v", st, err)
	}
	u, err := c.Invite(ctx, "r", "anna")
	if err != nil || u == "" {
		t.Fatalf("invite: %q %v", u, err)
	}
	if st, _ := c.Access(ctx, "r", "anna"); st != github.AccessInvited {
		t.Fatalf("after invite: %s", st)
	}
	f.collab["anna"] = "ready"
	if st, _ := c.Access(ctx, "r", "anna"); st != github.AccessReady {
		t.Fatalf("after accept: %s", st)
	}
	if u, err := c.Invite(ctx, "r", "anna"); err != nil || u != "" {
		t.Fatalf("invite of a collaborator: %q %v", u, err)
	}
}

func TestMergeErrorsAreDistinguishable(t *testing.T) {
	f, c := newFake(t)
	if err := c.Merge(ctx, "r", 3, "abc"); err != nil {
		t.Fatal(err)
	}
	f.mergeStatus = 409
	if err := c.Merge(ctx, "r", 3, "abc"); !errors.Is(err, github.ErrHeadChanged) {
		t.Fatalf("409: %v", err)
	}
	f.mergeStatus = 405
	if err := c.Merge(ctx, "r", 3, "abc"); !errors.Is(err, github.ErrNotMergeable) {
		t.Fatalf("405: %v", err)
	}
	f.mergeStatus = 502
	if err := c.Merge(ctx, "r", 3, "abc"); err == nil || errors.Is(err, github.ErrHeadChanged) || errors.Is(err, github.ErrNotMergeable) {
		t.Fatalf("502: %v", err)
	}
}

func TestChecksReviewPullAndTarball(t *testing.T) {
	f, c := newFake(t)
	id, url, err := c.CreateCheckRun(ctx, "r", "abc", "Официальная проверка", "attempt-1")
	if err != nil || id != 7 || url == "" || f.lastCheckRun["head_sha"] != "abc" || f.lastCheckRun["external_id"] != "attempt-1" {
		t.Fatalf("create check run: %d %q %v %+v", id, url, err, f.lastCheckRun)
	}
	if err := c.CompleteCheckRun(ctx, "r", id, "failure", "1 тест не прошёл", "test_sum: ожидалось 6"); err != nil || f.lastCheckRun["conclusion"] != "failure" {
		t.Fatalf("complete: %v %+v", err, f.lastCheckRun)
	}
	pr, err := c.PullRequest(ctx, "r", 3)
	if err != nil || pr.Head.SHA != "abc" || pr.Mergeable == nil || !*pr.Mergeable {
		t.Fatalf("pull: %+v %v", pr, err)
	}
	if err := c.Review(ctx, "r", 3, "abc", "APPROVE", "Все критерии выполнены"); err != nil || f.lastReview["commit_id"] != "abc" || f.lastReview["event"] != "APPROVE" {
		t.Fatalf("review: %v %+v", err, f.lastReview)
	}
	body, err := c.Tarball(ctx, "r", "abc")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(body)
	body.Close()
	if string(b) != "tar:abc" {
		t.Fatalf("tarball: %q", b)
	}
}

func TestInstallationTokenIsCached(t *testing.T) {
	f, c := newFake(t)
	for i := 0; i < 3; i++ {
		c.PullRequest(ctx, "r", 3)
	}
	if f.tokens != 1 {
		t.Fatalf("token requests: %d", f.tokens)
	}
	c.Now = func() time.Time { return time.Now().Add(59*time.Minute + 30*time.Second) }
	c.PullRequest(ctx, "r", 3)
	if f.tokens != 2 {
		t.Fatalf("token near expiry must be refreshed: %d", f.tokens)
	}
	found := false
	for _, op := range f.observed {
		found = found || op == "get_pull"
	}
	if !found {
		t.Fatalf("Observe not called: %v", f.observed)
	}
}

func TestTimeoutIsDistinguishable(t *testing.T) {
	_, c := newFake(t)
	cctx, cancel := context.WithTimeout(ctx, time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	if _, err := c.PullRequest(cctx, "r", 3); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
}

func TestOAuthExchange(t *testing.T) {
	_, c := newFake(t)
	if u := c.AuthorizeURL("st-1"); !strings.Contains(u, "client_id=cid") || !strings.Contains(u, "state=st-1") {
		t.Fatalf("authorize url: %s", u)
	}
	u, err := c.ExchangeCode(ctx, "good")
	if err != nil || u.ID != 99 || u.Login != "anna" {
		t.Fatalf("exchange: %+v %v", u, err)
	}
	if _, err := c.ExchangeCode(ctx, "bad"); err == nil {
		t.Fatal("bad code accepted")
	}
}

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !github.VerifySignature("s3cret", body, good) {
		t.Fatal("valid signature rejected")
	}
	flipped := good[:len(good)-1] + map[bool]string{true: "1", false: "0"}[good[len(good)-1] == '0']
	for _, bad := range []string{"", "sha256=00", "sha1=" + good[7:], flipped} {
		if github.VerifySignature("s3cret", body, bad) {
			t.Fatalf("invalid signature accepted: %q", bad)
		}
	}
	if github.VerifySignature("other", body, good) {
		t.Fatal("wrong secret accepted")
	}
}

func TestCheck(t *testing.T) {
	_, c := newFake(t)
	if slug, err := c.Check(ctx); err != nil || slug != "tutor-app" {
		t.Fatalf("check: %q %v", slug, err)
	}
	c.Org = "other"
	if _, err := c.Check(ctx); err == nil {
		t.Fatal("check passed for an organization without the App")
	}
}
