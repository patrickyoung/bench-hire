package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestRunKeepsJobDescriptionSeparateFromTask(t *testing.T) {
	a, jobs, _ := newTestApp(t)
	ctx := context.Background()
	w, _, err := a.createWorker(ctx, createWorkerRequest{Name: "Writer", Purpose: "Write useful notes."})
	if err != nil {
		t.Fatal(err)
	}
	first, err := a.intake(ctx, w, intakeRequest{Text: "Explain this week's results", Check: "test -d requests"})
	if err != nil {
		t.Fatal(err)
	}
	if code, err := jobs.Work(ctx); err != nil || code != 0 {
		t.Fatalf("first run: %d %v", code, err)
	}
	original, err := readExecutionEvidence(a.store.workerDir(w.Slug), first.Request.ID)
	if err != nil || len(original) != 1 || original[0].DefinitionStable == nil || !*original[0].DefinitionStable {
		t.Fatalf("missing stable run evidence: %+v %v", original, err)
	}
	if original[0].Definition.Purpose != w.Purpose || original[0].RequestSHA256 != contentSHA256([]byte(renderRequestFile(first.Request))) {
		t.Fatal("run did not record its standing job and exact task separately")
	}
	// A queued task keeps what was asked but uses the job description in force
	// when it starts. Earlier run records must keep the earlier definition.
	second, err := a.intake(ctx, w, intakeRequest{Text: "Explain next week's plan"})
	if err != nil {
		t.Fatal(err)
	}
	proposal := original[0].Definition
	proposal.Purpose = "Write concise notes with explicit next actions."
	proposal.Files.Agents += "\nEnd with a concrete next action.\n"
	proposal.Checks = []WorkerCheck{{Kind: "text_contains", Path: "requests/{request_id}/RESULT.md", Text: "Summary", Description: "Includes a summary marker"}}
	updated, err := a.applyAgentDefinition(ctx, w, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if code, err := jobs.Work(ctx); err != nil || code != 0 {
		t.Fatalf("second run: %d %v", code, err)
	}
	saved, err := a.store.Request(w.Slug, second.Request.ID)
	if err != nil || saved.Text != second.Request.Text || saved.Model != second.Request.Model {
		t.Fatal("changing the job description rewrote a task")
	}
	records, err := readExecutionEvidence(a.store.workerDir(w.Slug), second.Request.ID)
	if err != nil || len(records) != 1 || records[0].Definition.Purpose != updated.Purpose || len(records[0].Definition.Checks) != 1 {
		t.Fatalf("second run used the wrong definition: %+v %v", records, err)
	}
	evidence := a.taskEvidence(ctx, updated, first.Request)
	if len(evidence["checks"].([]WorkerCheck)) != 0 || len(evidence["runs"].([]ExecutionEvidence)) != 1 {
		t.Fatal("an old task displayed today's criteria")
	}
	// Unknown completion must not be accepted against a newly weakened check.
	jobs.MarkUnknown(second.Request.ID)
	check := filepath.Join(a.homeDir(w.Slug), "bin", "check")
	if err := os.WriteFile(check, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Resolve(ctx, second.Request.ID, "done"); err == nil {
		t.Fatal("unknown attempt was resolved using a different check")
	}
}

func TestDefinitionEditsCannotRaceExecutionOrRetirement(t *testing.T) {
	a, _, _ := newTestApp(t)
	ctx := context.Background()
	w, _, err := a.createWorker(ctx, createWorkerRequest{Name: "Writer", Purpose: "Write notes."})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := lockExecution(a.store.workerDir(w.Slug), false)
	if err != nil {
		t.Fatal(err)
	}
	h := a.routes()
	for _, route := range []string{"definition", "checks"} {
		body := map[string]any{"name": "AGENTS.md", "content": "Changed instructions"}
		if route == "checks" {
			body = map[string]any{"checks": []WorkerCheck{}}
		}
		rec, _ := call(t, h, http.MethodPut, "/api/workers/writer/"+route, body)
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s edit while running: %d %s", route, rec.Code, rec.Body.String())
		}
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	def, err := readAgentDefinition(a.homeDir(w.Slug), w)
	if err != nil {
		t.Fatal(err)
	}
	// A builder may hold a stale worker value from before a pause or model edit.
	w.Model, w.Enabled = "openai/another-model", false
	if err := a.store.SaveWorker(w); err != nil {
		t.Fatal(err)
	}
	stale := w
	stale.Enabled, stale.Model = true, "openai/old-model"
	updated, err := a.applyAgentDefinition(ctx, stale, def)
	if err != nil || updated.Enabled || updated.Model != w.Model {
		t.Fatalf("apply overwrote current settings: %+v %v", updated, err)
	}
	if _, err := a.retireWorker(ctx, w.Slug, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.applyAgentDefinition(ctx, stale, def); err == nil {
		t.Fatal("stale builder resurrected a retired worker")
	}
	rec, _ := call(t, h, http.MethodPost, "/api/workers/writer/update", map[string]any{"name": "Resurrected"})
	if rec.Code != http.StatusConflict {
		t.Fatal("retired worker accepted settings changes")
	}
}
