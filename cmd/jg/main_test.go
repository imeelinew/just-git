package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s (%v)", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func writeTest(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func repoTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitTest(t, dir, "init", "-b", "main")
	gitTest(t, dir, "config", "user.name", "Test")
	gitTest(t, dir, "config", "user.email", "test@example.com")
	gitTest(t, dir, "config", "commit.gpgsign", "false")
	gitTest(t, dir, "config", "core.hooksPath", filepath.Join(dir, ".git", "hooks"))
	writeTest(t, filepath.Join(dir, "tracked.txt"), "before\n")
	writeTest(t, filepath.Join(dir, "deleted.txt"), "remove me\n")
	writeTest(t, filepath.Join(dir, ".gitignore"), "ignored.txt\n")
	gitTest(t, dir, "add", "-A")
	gitTest(t, dir, "commit", "-m", "Initial commit")
	return dir
}

func configureTest(t *testing.T, url string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("JG_BASE_URL", url)
	t.Setenv("JG_API_KEY", "test-key")
	t.Setenv("JG_MODEL", "test-model")
}

func TestCommitAndPush(t *testing.T) {
	for _, action := range []string{"commit", "push"} {
		t.Run(action, func(t *testing.T) {
			dir := repoTest(t)
			remote := t.TempDir()
			gitTest(t, remote, "init", "--bare", "-b", "main")
			gitTest(t, dir, "remote", "add", "origin", remote)
			gitTest(t, dir, "push", "-u", "origin", "main")
			before := gitTest(t, remote, "rev-parse", "main")
			writeTest(t, filepath.Join(dir, "tracked.txt"), "after\n")
			writeTest(t, filepath.Join(dir, "new.txt"), "new file\n")
			writeTest(t, filepath.Join(dir, "ignored.txt"), "private\n")
			if err := os.Remove(filepath.Join(dir, "deleted.txt")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
				t.Fatal(err)
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
				}
				var body struct {
					Model    string              `json:"model"`
					Messages []map[string]string `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Model != "test-model" || len(body.Messages) != 2 {
					t.Errorf("Unexpected payload: %+v", body)
				} else {
					diff := body.Messages[1]["content"]
					for _, name := range []string{"tracked.txt", "new.txt", "deleted.txt", "+after", "-before"} {
						if !strings.Contains(diff, name) {
							t.Errorf("Diff missing %q", name)
						}
					}
					if strings.Contains(diff, "private") {
						t.Error("Included ignored file")
					}
				}
				w.Write([]byte(`{"choices":[{"message":{"content":"Update tracked content and replace obsolete files"}}]}`))
			}))
			defer server.Close()
			configureTest(t, server.URL+"/v1/")
			var out, stderr bytes.Buffer
			if err := run(context.Background(), filepath.Join(dir, "nested"), []string{action}, &out, &stderr); err != nil {
				t.Fatalf("run: %v\n%s", err, &stderr)
			}
			if calls != 1 {
				t.Fatalf("API calls: %d", calls)
			}
			if got := gitTest(t, dir, "log", "-1", "--format=%s"); got != "Update tracked content and replace obsolete files" {
				t.Fatal(got)
			}
			if got := gitTest(t, dir, "status", "--porcelain"); got != "" {
				t.Fatal(got)
			}
			local := gitTest(t, dir, "rev-parse", "HEAD")
			pushed := gitTest(t, remote, "rev-parse", "main")
			if action == "push" && pushed != local {
				t.Fatal("Commit not pushed")
			}
			if action == "commit" && pushed != before {
				t.Fatal("Unexpected push")
			}
		})
	}
}

func TestGitErrorsPassThrough(t *testing.T) {
	for _, kind := range []string{"outside", "empty", "hook", "push"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			args := []string{"commit"}
			if kind != "outside" {
				dir = repoTest(t)
			}
			if kind == "hook" || kind == "push" {
				writeTest(t, filepath.Join(dir, "tracked.txt"), "changed\n")
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Write([]byte(`{"choices":[{"message":{"content":"Update tracked content"}}]}`))
				}))
				defer server.Close()
				configureTest(t, server.URL)
			}
			if kind == "hook" {
				path := filepath.Join(dir, ".git", "hooks", "pre-commit")
				writeTest(t, path, "#!/bin/sh\necho 'Hook rejected commit' >&2\nexit 1\n")
				if err := os.Chmod(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "push" {
				args[0] = "push"
			}
			var out, stderr bytes.Buffer
			err := run(context.Background(), dir, args, &out, &stderr)
			var gitErr *exec.ExitError
			if !errors.As(err, &gitErr) {
				t.Fatalf("Expected Git exit error, got %v", err)
			}
			switch kind {
			case "outside":
				cmd := exec.Command("git", "rev-parse", "--show-toplevel")
				cmd.Dir = dir
				want, _ := cmd.CombinedOutput()
				if stderr.String() != string(want) {
					t.Fatalf("Changed error: %q", stderr.String())
				}
			case "empty":
				if !strings.Contains(out.String(), "nothing to commit") {
					t.Fatal(out.String())
				}
			case "hook":
				if stderr.String() != "Hook rejected commit\n" {
					t.Fatal(stderr.String())
				}
			case "push":
				if !strings.Contains(stderr.String(), "No configured push destination") {
					t.Fatal(stderr.String())
				}
				if got := gitTest(t, dir, "log", "-1", "--format=%s"); got != "Update tracked content" {
					t.Fatal(got)
				}
			}
		})
	}
}

func TestGenerationFailureLeavesChangesStaged(t *testing.T) {
	dir := repoTest(t)
	before := gitTest(t, dir, "rev-parse", "HEAD")
	writeTest(t, filepath.Join(dir, "tracked.txt"), "changed\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"Invalid key"}`))
	}))
	defer server.Close()
	configureTest(t, server.URL)
	err := run(context.Background(), dir, []string{"commit"}, io.Discard, io.Discard)
	if err == nil || err.Error() != `401 Unauthorized: {"error":"Invalid key"}` {
		t.Fatal(err)
	}
	if got := gitTest(t, dir, "rev-parse", "HEAD"); got != before {
		t.Fatal("Unexpected commit")
	}
	if got := gitTest(t, dir, "diff", "--cached", "--name-only"); got != "tracked.txt" {
		t.Fatal(got)
	}
}

func TestConfigOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.Mkdir(filepath.Join(dir, "just-git"), 0700); err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(dir, "just-git", "config.json"), `{"base_url":"http://local/v1","api_key":"file-key","model":"file-model","thinking":"disabled","reasoning_effort":"high"}`)
	t.Setenv("JG_BASE_URL", "http://override/v1")
	t.Setenv("JG_API_KEY", "")
	t.Setenv("JG_MODEL", "override-model")
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "http://override/v1" || c.APIKey != "" || c.Model != "override-model" || c.Thinking != "disabled" || c.ReasoningEffort != "high" {
		t.Fatalf("%+v", c)
	}
}

func TestThinkingModes(t *testing.T) {
	for _, mode := range []string{"", "enabled", "disabled"} {
		for _, effort := range []string{"", "low", "high", "max", "medium"} {
			t.Run("mode="+mode+"/effort="+effort, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					value, exists := body["thinking"]
					if mode == "" {
						if exists {
							t.Error("Default mode must omit thinking")
						}
					} else if string(value) != `{"type":"`+mode+`"}` {
						t.Errorf("Unexpected thinking parameter: %s", value)
					}
					value, exists = body["reasoning_effort"]
					if mode == "disabled" || effort == "" {
						if exists {
							t.Error("Effort must be omitted")
						}
					} else if string(value) != `"`+effort+`"` {
						t.Errorf("Unexpected reasoning effort: %s", value)
					}
					w.Write([]byte(`{"choices":[{"message":{"reasoning_content":"Internal reasoning","content":"Add configurable thinking mode"}}]}`))
				}))
				defer server.Close()
				message, err := generate(context.Background(), server.Client(), config{providerConfig: providerConfig{BaseURL: server.URL, Model: "test", Thinking: mode, ReasoningEffort: effort}}, "diff")
				if err != nil || message != "Add configurable thinking mode" {
					t.Fatalf("message=%q error=%v", message, err)
				}
			})
		}
	}
}

func TestInvalidMessages(t *testing.T) {
	for _, response := range []string{`{"choices":[]}`, `{"choices":[{"message":{"content":""}}]}`, `{"choices":[{"message":{"content":"Title\nBody"}}]}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(response)) }))
		_, err := generate(context.Background(), server.Client(), config{providerConfig: providerConfig{BaseURL: server.URL, Model: "test"}}, "diff")
		server.Close()
		if err == nil {
			t.Fatalf("Accepted %s", response)
		}
	}
}

func TestGenerationTimeout(t *testing.T) {
	for _, flushHeaders := range []bool{false, true} {
		t.Run(fmt.Sprintf("headers=%t", flushHeaders), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if flushHeaders {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err := generate(ctx, server.Client(), config{providerConfig: providerConfig{BaseURL: server.URL, Model: "test"}}, "diff")
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Expected deadline exceeded, got %v", err)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderFallback(t *testing.T) {
	for _, status := range []int{200, 400, 401, 403, 404, 408, 413, 422, 429, 500, 502, 503, 504} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			primaryCalls, fallbackCalls := 0, 0
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				primaryCalls++
				w.WriteHeader(status)
				io.WriteString(w, `{"choices":[{"message":{"content":"Use primary provider"}}]}`)
			}))
			defer primary.Close()
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fallbackCalls++
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer backup-key" || string(body["model"]) != `"backup-model"` || string(body["thinking"]) != `{"type":"disabled"}` {
					t.Errorf("Fallback did not use its own configuration: %s %v", r.URL.Path, body)
				}
				if _, ok := body["reasoning_effort"]; ok {
					t.Error("Leaked primary reasoning effort")
				}
				io.WriteString(w, `{"choices":[{"message":{"content":"Use fallback provider"}}]}`)
			}))
			defer fallback.Close()
			c := config{
				providerConfig: providerConfig{BaseURL: primary.URL, Model: "primary", Thinking: "enabled", ReasoningEffort: "high"},
				Fallback:       &providerConfig{BaseURL: fallback.URL + "/v1", APIKey: "backup-key", Model: "backup-model", Thinking: "disabled"},
			}
			for i := 0; i < 2; i++ {
				var progress bytes.Buffer
				message, err := generateWithFallback(context.Background(), primary.Client(), c, "diff", &progress)
				switch {
				case status == 200:
					if err != nil || message != "Use primary provider" {
						t.Fatalf("%q %v", message, err)
					}
				case status == 408 || status >= 500:
					if err != nil || message != "Use fallback provider" || !strings.Contains(progress.String(), "trying fallback") {
						t.Fatalf("%q %v %s", message, err, &progress)
					}
				default:
					if err == nil {
						t.Fatal("Expected primary error")
					}
				}
			}
			wantFallback := 0
			if status == 408 || status >= 500 {
				wantFallback = 2
			}
			if primaryCalls != 2 || fallbackCalls != wantFallback {
				t.Fatalf("primary=%d fallback=%d", primaryCalls, fallbackCalls)
			}
		})
	}
}

func TestFallbackTransportAndCancellation(t *testing.T) {
	for _, kind := range []string{"connection", "timeout", "body-timeout", "canceled", "parent-deadline", "invalid-request", "invalid-message", "both-fail", "no-fallback"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			if kind == "canceled" || kind == "parent-deadline" {
				var cancel context.CancelFunc
				if kind == "canceled" {
					ctx, cancel = context.WithCancel(ctx)
				} else {
					ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				}
				cancel()
			}
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					switch kind {
					case "connection", "both-fail", "no-fallback":
						return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
					case "timeout", "parent-deadline":
						return nil, context.DeadlineExceeded
					case "body-timeout":
						return &http.Response{StatusCode: 200, Body: timeoutBody{}, Header: make(http.Header)}, nil
					case "canceled":
						return nil, context.Canceled
					case "invalid-request":
						return nil, errors.New("invalid request header")
					case "invalid-message":
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[]}`)), Header: make(http.Header)}, nil
					}
				}
				if r.Context().Err() != nil {
					t.Error("Fallback inherited expired request context")
				}
				if kind == "both-fail" {
					return nil, errors.New("backup unavailable")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"Use backup"}}]}`)), Header: make(http.Header)}, nil
			})}
			c := config{providerConfig: providerConfig{BaseURL: "http://primary", Model: "primary"}, Fallback: &providerConfig{BaseURL: "http://backup", Model: "backup"}}
			if kind == "no-fallback" {
				c.Fallback = nil
			}
			message, err := generate(ctx, client, c, "diff")
			shouldFallback := kind == "connection" || kind == "timeout" || kind == "body-timeout" || kind == "both-fail"
			wantCalls := 1
			if shouldFallback {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d want=%d", calls, wantCalls)
			}
			if shouldFallback && kind != "both-fail" {
				if err != nil || message != "Use backup" {
					t.Fatalf("%q %v", message, err)
				}
			} else if err == nil {
				t.Fatal("Expected error")
			}
			if kind == "both-fail" && (!strings.Contains(err.Error(), "connection refused") || !strings.Contains(err.Error(), "backup unavailable")) {
				t.Fatal(err)
			}
		})
	}
}

type timeoutBody struct{}

func (timeoutBody) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }
func (timeoutBody) Close() error             { return nil }

func TestFallbackConfig(t *testing.T) {
	configureTest(t, "http://primary/v1")
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "just-git")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, fallback := range []string{`null`, `{"base_url":"https://backup/v1","model":"backup","api_key":"backup-key","thinking":"enabled","reasoning_effort":"low"}`, `{}`, `{"base_url":"ftp://backup","model":"backup"}`, `{"base_url":"https://backup","model":"backup","thinking":"invalid"}`} {
		writeTest(t, filepath.Join(dir, "config.json"), `{"fallback":`+fallback+`}`)
		c, err := loadConfig()
		valid := fallback == "null" || strings.Contains(fallback, "backup-key")
		if valid && err != nil {
			t.Fatal(err)
		}
		if !valid && (err == nil || !strings.HasPrefix(err.Error(), "fallback:")) {
			t.Fatalf("%s: %v", fallback, err)
		}
		if valid && c.Fallback != nil && (c.Fallback.Model != "backup" || c.Fallback.APIKey != "backup-key" || c.Fallback.Thinking != "enabled" || c.Fallback.ReasoningEffort != "low") {
			t.Fatalf("Unexpected fallback: %+v", c.Fallback)
		}
	}
}

func TestCertificateErrorDoesNotTriggerFallback(t *testing.T) {
	err := &url.Error{Op: "Post", URL: "https://primary", Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}}
	var failure *providerFailure
	if errors.As(transportFailure(err), &failure) {
		t.Fatal("Certificate error triggered fallback")
	}
}
