package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This is an opt-in test of the rendered application in a fresh browser
// profile. The HTTP/controller/UI code is real; execution uses offline
// fixtures. It never touches an operator's browser, workers, or credentials.
func TestBrowserManagerFlow(t *testing.T) {
	runBrowserFlow(t, "tests/manager-browser.js")
}

func TestBrowserConnectedAppsFlow(t *testing.T) {
	for _, viewport := range []string{"1280,1000", "390,844", "320,740"} {
		t.Run(viewport, func(t *testing.T) { runBrowserFlow(t, "tests/connections-browser.js", viewport) })
	}
}

func TestBrowserUploadsFlow(t *testing.T) {
	for _, viewport := range []string{"1280,1000", "390,844", "320,740"} {
		t.Run(viewport, func(t *testing.T) { runBrowserFlow(t, "tests/uploads-browser.js", viewport) })
	}
}

func TestBrowserSkillImprovementFlow(t *testing.T) {
	for _, viewport := range []string{"1280,1000", "390,844", "320,740"} {
		t.Run(viewport, func(t *testing.T) { runBrowserFlow(t, "tests/skills-browser.js", viewport) })
	}
}

func TestBrowserLearningFlow(t *testing.T) {
	runBrowserFlow(t, "tests/learning-browser.js")
}

func TestBrowserWorkspaceFlow(t *testing.T) {
	for _, viewport := range []struct{ name, size string }{{"desktop", "1440,1100"}, {"mobile", "390,844"}, {"small-mobile", "320,740"}} {
		t.Run(viewport.name, func(t *testing.T) { runBrowserFlow(t, "tests/workspace-browser.js", viewport.size) })
	}
}

func TestBrowserReadingFlow(t *testing.T) {
	for _, viewport := range []struct{ name, size string }{{"desktop", "1280,1000"}, {"mobile", "480,960"}} {
		t.Run(viewport.name, func(t *testing.T) { runBrowserFlow(t, "tests/reading-browser.js", viewport.size) })
	}
}

func runBrowserFlow(t *testing.T, scriptPath string, viewport ...string) {
	t.Helper()
	if os.Getenv("HIRE_BROWSER") != "1" {
		t.Skip("set HIRE_BROWSER=1 to run the isolated Chromium UI test")
	}
	chrome, err := exec.LookPath("chromium")
	if err != nil {
		t.Fatal("Chromium is required for HIRE_BROWSER=1")
	}
	a, jobs, _ := newTestApp(t)
	if strings.HasSuffix(scriptPath, "learning-browser.js") {
		configureLearningFixture(t, a)
	}
	a.assets, err = fs.Sub(webAssets, "web")
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.SaveModelProof(ModelProof{Model: a.model, OK: true, Output: "ok", At: a.now()}); err != nil {
		t.Fatal(err)
	}
	callLog := configureBuilderFixture(t, a, true)
	t.Setenv("FAKE_BUILDER_SYNTHESIS_REPLY", writeTestReply(t, t.TempDir(), "worker.json", strings.Replace(fixtureWorkerProposal, `"name":"Notes"`, `"name":"Test Writer"`, 1)))
	if strings.HasSuffix(scriptPath, "uploads-browser.js") {
		configureSourceFixture(t, a)
		a.tools.paths["ask"] = writeScript(t, t.TempDir(), "ask", sourceAskFixture)
	}
	if strings.HasSuffix(scriptPath, "skills-browser.js") {
		configureSkillImprovementFixture(t, a)
	}
	if strings.HasSuffix(scriptPath, "connections-browser.js") {
		connectedWorkerFixture(t, a)
	}
	app := a.routes()
	results := make(chan string, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__test/flow.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write(script)
			return
		case "/__test/pulse":
			select {
			case <-time.After(10 * time.Millisecond):
			case <-r.Context().Done():
			}
			w.WriteHeader(204)
			return
		case "/__test/work":
			code, err := jobs.Work(r.Context())
			if err != nil || code != 0 {
				http.Error(w, fmt.Sprint(code, err), 500)
				return
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		case "/__test/result":
			var result struct {
				Error  string   `json:"error"`
				Checks []string `json:"checks"`
			}
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			select {
			case results <- result.Error:
			default:
			}
			writeJSON(w, 200, result)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/assets/") {
			app.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, r)
		for name, values := range rec.Header() {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.Header().Del("Content-Length")
		w.WriteHeader(rec.Code)
		_, _ = w.Write([]byte(strings.Replace(rec.Body.String(), "</body>", "<script src=\"/__test/flow.js\" defer></script></body>", 1)))
	}))
	a.host = server.Listener.Addr().String()
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	size := "1280,1000"
	if len(viewport) > 0 {
		size = viewport[0]
	}
	args := []string{"--headless", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--disable-background-networking", "--disable-component-update", "--disable-extensions", "--disable-sync", "--no-first-run", "--no-default-browser-check", "--password-store=basic", "--user-data-dir=" + filepath.Join(t.TempDir(), "profile"), "--window-size=" + size, "--virtual-time-budget=40000", "--timeout=40000", "--dump-dom"}
	if screenshot := os.Getenv("HIRE_BROWSER_SCREENSHOT"); screenshot != "" {
		ext := filepath.Ext(screenshot)
		name := strings.ReplaceAll(t.Name(), "/", "-")
		args = append(args, "--run-all-compositor-stages-before-draw", "--screenshot="+strings.TrimSuffix(screenshot, ext)+"-"+name+ext)
	}
	args = append(args, server.URL)
	cmd := exec.CommandContext(ctx, chrome, args...)
	output, err := cmd.CombinedOutput()
	select {
	case failure := <-results:
		if failure != "" {
			t.Fatalf("browser: %s\n%s", failure, output)
		}
		if err != nil {
			t.Fatalf("Chromium: %v\n%s", err, output)
		}
	default:
		t.Fatalf("browser test did not finish: %v\n%s", err, output)
	}
	calls, err := os.ReadFile(callLog)
	if !strings.HasSuffix(scriptPath, "manager-browser.js") {
		if !os.IsNotExist(err) || len(calls) != 0 {
			t.Fatalf("learning fixture unexpectedly called Ask: %v", err)
		}
	} else if err != nil || strings.Contains(string(calls), "builder-router-schema.json") || strings.Contains(string(calls), "builder-expert-schema.json") {
		t.Fatalf("ordinary browser hire did not use the single author: %v", err)
	}
}
