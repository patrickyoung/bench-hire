package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPinnedToolsStayTogether(t *testing.T) {
	dir := t.TempDir()
	agent := writeScript(t, dir, "agent", "#!/bin/sh\nprintf '%s\\n' \"$AGENT_HONE\" \"$AGENT_BRIEF\"\n")
	hone := writeScript(t, dir, "hone", "#!/bin/sh\nexit 0\n")
	brief := writeScript(t, dir, "brief", "#!/bin/sh\nexit 0\n")
	for _, name := range append(append([]string{}, requiredTools...), companionTools...) {
		t.Setenv("HIRE_"+strings.ReplaceAll(strings.ToUpper(name), "-", "_"), "")
	}
	t.Setenv("AGENT_HONE", "/wrong/hone")
	tools := newToolset(dir)
	if tools.path("agent") != agent || tools.path("ask") != "" {
		t.Fatal("a missing pinned tool fell back to the host PATH")
	}
	stdout, stderr, code, err := tools.run(context.Background(), "agent", nil, "", nil, 5e9)
	if err != nil || code != 0 || string(stdout) != hone+"\n"+brief+"\n" {
		t.Fatalf("wrong child suite: %s %s %d %v", stdout, stderr, code, err)
	}
}

func TestRealSuiteExampleHomes(t *testing.T) {
	bin := os.Getenv("HIRE_INTEGRATION_BIN_DIR")
	if bin == "" {
		t.Skip("set HIRE_INTEGRATION_BIN_DIR for real Bench composition tests")
	}
	bin, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := newTestApp(t)
	for _, name := range append(append([]string{}, requiredTools...), companionTools...) {
		t.Setenv("HIRE_"+strings.ReplaceAll(strings.ToUpper(name), "-", "_"), "")
	}
	a.tools = newToolset(bin)
	for _, id := range []string{"reporting", "documents", "maintenance"} {
		worker, _, err := a.createWorker(context.Background(), createWorkerRequest{ExampleID: id})
		if err != nil || !worker.Receipt.Valid {
			t.Fatalf("real Agent rejected %s: %+v %v", id, worker.Receipt, err)
		}
	}
}

// The model endpoint is a deterministic local fixture; every Bench program,
// its session format, confinement, queue, replay and learning check is real.
// This demonstrates composition, not the quality of any live model's lessons.
func TestRealSuiteRecoveryAndLearning(t *testing.T) {
	runRealSuiteRecovery(t, false)
}

func TestRealSuiteSpecialist(t *testing.T) {
	runRealSuiteRecovery(t, true)
}

func runRealSuiteRecovery(t *testing.T, specialist bool) {
	t.Helper()
	bin := os.Getenv("HIRE_INTEGRATION_BIN_DIR")
	if bin == "" {
		t.Skip("set HIRE_INTEGRATION_BIN_DIR for real Bench composition tests")
	}
	bin, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := newTestApp(t)
	for _, name := range append(append([]string{}, requiredTools...), companionTools...) {
		t.Setenv("HIRE_"+strings.ReplaceAll(strings.ToUpper(name), "-", "_"), "")
	}
	a.tools = newToolset(bin)
	for _, entry := range a.tools.agentEnvironment() {
		name, value, _ := strings.Cut(entry, "=")
		t.Setenv(name, value)
	}
	t.Setenv("HIRE_AGENT", a.tools.path("agent"))
	a.model = "openai/fixture"
	var calls atomic.Int32
	replies := []string{
		"```sh\nid=$(sed -n 's/^id: //p' ../REQUEST.md | head -1)\nmkdir -p \"requests/$id\"\nprintf 'draft result\\n' > \"requests/$id/RESULT.md\"\nfalse\n```",
		"```sh\nid=$(sed -n 's/^id: //p' ../REQUEST.md | head -1)\nprintf 'verified result\\n' > \"requests/$id/RESULT.md\"\n```",
		"The verified result is written.",
		"- Read the current request's check before writing the result; a draft marker does not meet its required verified marker.",
		"Use when delivering checked reports from a current request.",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1)) - 1
		if n >= len(replies) {
			t.Errorf("unexpected model call %d", n+1)
			http.Error(w, "fixture exhausted", 400)
			return
		}
		item := map[string]any{"type": "message", "id": fmt.Sprintf("msg_%d", n), "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": replies[n], "annotations": []any{}}}}
		complete := map[string]any{"type": "response.completed", "sequence_number": 2, "response": map[string]any{
			"id": fmt.Sprintf("resp_%d", n), "object": "response", "created_at": 1, "status": "completed", "model": "fixture", "output": []any{item},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 20, "total_tokens": 30, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "input_tokens_details": map[string]any{"cached_tokens": 0}},
		}}
		data, _ := json.Marshal(complete)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", data)
	}))
	defer server.Close()
	t.Setenv("OPENAI_BASE_URL", server.URL)
	t.Setenv("OPENAI_API_KEY", "offline-fixture")
	t.Setenv("ASK_MODEL", a.model)
	jobs, err := newTendJobs(a.tools.path("tend"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.jobs = jobs
	ctx := context.Background()
	w, _, err := a.createWorker(ctx, createWorkerRequest{Name: "Recovery writer", Purpose: "Write a checked report."})
	if err != nil {
		t.Fatal(err)
	}
	if w.CheckState != "valid" {
		t.Fatalf("home: %s", w.CheckMessage)
	}
	in := intakeRequest{Text: "Write the report with the verified marker.", Check: "grep -q verified requests/*/RESULT.md"}
	if specialist {
		rec, _ := call(t, a.routes(), http.MethodPost, "/api/workers/"+w.Slug+"/specialists", map[string]any{"name": "reviewer", "purpose": "Independently check the report against its evidence."})
		if rec.Code != http.StatusCreated {
			t.Fatalf("real specialist creation: %d %s", rec.Code, rec.Body.String())
		}
		in.Specialist = "reviewer"
	}
	result, err := a.intake(ctx, w, in)
	if err != nil {
		t.Fatal(err)
	}
	if code, err := jobs.Work(ctx); err != nil || code != 0 {
		attempts, _ := jobs.Attempts(ctx, result.Request.ID)
		t.Fatalf("real run: %d %v\n%+v", code, err, attempts)
	}
	if calls.Load() != 3 {
		t.Fatalf("work model calls=%d, want 3", calls.Load())
	}
	home, err := requestHome(a.homeDir(w.Slug), result.Request)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := filepath.Glob(filepath.Join(home, ".agent", "runs", "*.jsonl"))
	if err != nil || len(sessions) != 1 {
		t.Fatalf("run sessions: %v %v", sessions, err)
	}
	if specialist {
		parentSessions, _ := filepath.Glob(filepath.Join(a.homeDir(w.Slug), ".agent", "runs", "*.jsonl"))
		if len(parentSessions) != 0 {
			t.Fatal("specialist reused the parent's conversation evidence")
		}
		records, err := readExecutionEvidence(a.store.workerDir(w.Slug), result.Request.ID)
		if err != nil || len(records) != 1 || records[0].Definition.Name != "reviewer" {
			t.Fatalf("real specialist evidence: %+v %v", records, err)
		}
		if code, _, stderr, err := a.jobsResolveForTest(ctx, result.Request.ID); code != 0 || err != nil {
			t.Fatalf("real specialist verify: %d %s %v", code, stderr, err)
		}
		rec, payload := call(t, a.routes(), http.MethodGet, "/api/workers/"+w.Slug+"/requests/"+result.Request.ID, nil)
		if rec.Code != http.StatusOK || !strings.Contains(payload["result"].(map[string]any)["content"].(string), "verified result") {
			t.Fatalf("real specialist result: %d %s", rec.Code, rec.Body.String())
		}
		return
	}
	h := a.routes()
	url := "/api/workers/" + w.Slug
	rec, payload := call(t, h, http.MethodGet, url+"/history", nil)
	if rec.Code != 200 || len(payload["entries"].([]any)) == 0 {
		t.Fatalf("real Trail history: %d %s", rec.Code, rec.Body.String())
	}
	for _, operation := range []string{"inspect", "prepare"} {
		rec, payload = call(t, h, http.MethodPost, url+"/learning", map[string]any{"operation": operation, "skill": "checked-reports", "session": filepath.Base(sessions[0])})
		if rec.Code != 200 {
			t.Fatalf("real learning %s: %d %s", operation, rec.Code, rec.Body.String())
		}
		if operation == "inspect" && calls.Load() != 3 {
			t.Fatal("inspecting recovery called a model")
		}
	}
	proposal := payload["proposal"].(string)
	skill := filepath.Join(home, "skills", "checked-reports", "SKILL.md")
	if _, err := os.Stat(skill); !os.IsNotExist(err) {
		t.Fatal("preparing a lesson installed it before review")
	}
	rec, payload = call(t, h, http.MethodGet, url+"/learning/"+proposal, nil)
	if rec.Code != 200 || !strings.Contains(payload["output"].(string), "Read the current request") {
		t.Fatalf("review exact lesson: %d %s", rec.Code, rec.Body.String())
	}
	before := calls.Load()
	rec, _ = call(t, h, http.MethodPost, url+"/learning", map[string]any{"operation": "admit", "proposal": proposal, "sha256": payload["sha256"]})
	if rec.Code != 200 {
		t.Fatalf("admit exact lesson: %d %s", rec.Code, rec.Body.String())
	}
	if calls.Load() != before {
		t.Fatal("admitting an exact lesson called a model")
	}
	content, err := os.ReadFile(skill)
	if err != nil || !strings.Contains(string(content), "Read the current request") {
		t.Fatalf("skill not installed: %s %v", content, err)
	}
	rec, payload = call(t, h, http.MethodGet, url+"/capabilities", nil)
	if rec.Code != 200 || len(payload["skills"].([]any)) != 1 {
		t.Fatalf("Brief cannot discover learned skill: %d %s", rec.Code, rec.Body.String())
	}
}
