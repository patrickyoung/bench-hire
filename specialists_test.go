package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSpecialistTaskIsolationAndRetirement(t *testing.T) {
	a, jobs, _ := newTestApp(t)
	ctx := context.Background()
	w, _, err := a.createWorker(ctx, createWorkerRequest{Name: "Reporter", Purpose: "Write reports.", Network: true})
	if err != nil {
		t.Fatal(err)
	}
	h := a.routes()
	url := "/api/workers/" + w.Slug
	role := "Review figures against signed source records. Explain missing evidence."
	rec, _ := call(t, h, http.MethodPost, url+"/specialists", map[string]any{"name": "reviewer", "purpose": role})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	home := a.homeDir(w.Slug)
	child := filepath.Join(home, "agents", "reviewer")
	definition, err := os.ReadFile(filepath.Join(child, "GOAL.md"))
	if err != nil || !strings.Contains(string(definition), role) {
		t.Fatalf("standing role: %s %v", definition, err)
	}
	requests, _ := a.store.Requests(w.Slug)
	if len(requests) != 0 {
		t.Fatal("creating a perspective started work")
	}
	rec, _ = call(t, h, http.MethodPost, url+"/specialists", map[string]any{"name": "reviewer", "purpose": "Replace existing role"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate: %d", rec.Code)
	}
	unchanged, _ := os.ReadFile(filepath.Join(child, "GOAL.md"))
	if string(unchanged) != string(definition) {
		t.Fatal("duplicate creation replaced the role")
	}
	// A parent's current request must remain untouched by a specialist run.
	parentRequest := []byte("A different request for the parent.\n")
	if err := os.WriteFile(filepath.Join(home, "REQUEST.md"), parentRequest, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := a.intake(ctx, w, intakeRequest{Text: "Check this month's signed figures.", Specialist: "reviewer", Check: "grep -q body requests/*/RESULT.md"})
	if err != nil {
		t.Fatal(err)
	}
	r := result.Request
	if r.Specialist != "reviewer" || r.Network {
		t.Fatalf("wrong task identity or inherited authority: %+v", r)
	}
	job, _ := jobs.Show(ctx, r.ID)
	if job.Cwd != home {
		t.Fatal("specialist escaped parent queue grouping")
	}
	wantArgs := []string{"specialist", home, "reviewer", "-m", w.Model, "-checkpoint", r.ID, "--", focusText(r)}
	if got := agentArgs(r, home); !reflect.DeepEqual(got, wantArgs) {
		t.Fatalf("public specialist argv: %q", got)
	}
	if code, err := jobs.Work(ctx); err != nil || code != 0 {
		t.Fatalf("work: %d %v", code, err)
	}
	current, _ := os.ReadFile(filepath.Join(home, "REQUEST.md"))
	if string(current) != string(parentRequest) {
		t.Fatal("specialist overwrote the parent's request")
	}
	if _, err := os.Stat(filepath.Join(home, "work", "requests", r.ID, "RESULT.md")); !os.IsNotExist(err) {
		t.Fatal("specialist result was written under parent work")
	}
	if _, err := resultDigest(child, r.ID); err != nil {
		t.Fatal(err)
	}
	evidence, err := readExecutionEvidence(a.store.workerDir(w.Slug), r.ID)
	if err != nil || len(evidence) != 1 || evidence[0].Definition.Name != "reviewer" || evidence[0].Definition.Files.Goal != string(definition) {
		t.Fatalf("specialist evidence: %+v %v", evidence, err)
	}
	if code, _, stderr, err := a.jobsResolveForTest(ctx, r.ID); err != nil || code != 0 {
		t.Fatalf("specialist verify: %d %s %v", code, stderr, err)
	}
	rec, payload := call(t, h, http.MethodGet, url+"/requests/"+r.ID, nil)
	if rec.Code != http.StatusOK || payload["resultPath"] != requestResultPath(r) {
		t.Fatalf("result view: %d %s", rec.Code, rec.Body.String())
	}
	request := payload["request"].(map[string]any)
	jobView := payload["job"].(map[string]any)
	review := map[string]any{"decision": "changes-requested", "note": "Explain the source date.", "resultSha256": request["resultSha256"], "jobUpdatedUs": jobView["updated_us"]}
	rec, payload = call(t, h, http.MethodPost, url+"/requests/"+r.ID+"/review", review)
	if rec.Code != http.StatusCreated {
		t.Fatalf("review: %d %s", rec.Code, rec.Body.String())
	}
	reviewID := payload["review"].(map[string]any)["id"].(string)
	revision, err := a.createRevision(ctx, w.Slug, r.ID, reviewID)
	if err != nil || revision.Specialist != r.Specialist || revision.Network {
		t.Fatalf("revision switched specialist/authority: %+v %v", revision, err)
	}
	// The reviewed perspective does not establish that the parent is useful.
	review["decision"], review["note"] = "accepted", "Useful perspective."
	rec, _ = call(t, h, http.MethodPost, url+"/requests/"+r.ID+"/review", review)
	if rec.Code != http.StatusCreated {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	_, payload = call(t, h, http.MethodGet, url, nil)
	if payload["accepted"] != float64(0) || payload["firstAcceptedAt"] != nil {
		t.Fatal("a specialist result established parent readiness")
	}
	// Resolve checks only the original child's request and recorded check.
	if err := os.WriteFile(filepath.Join(child, "REQUEST.md"), []byte("wrong task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _, _ := a.jobsResolveForTest(ctx, r.ID); code != 1 {
		t.Fatalf("wrong specialist request resolved: %d", code)
	}
	queued, err := a.intake(ctx, w, intakeRequest{Text: "Another perspective", Specialist: "reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	rec, _ = call(t, h, http.MethodDelete, url, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("retire: %d %s", rec.Code, rec.Body.String())
	}
	job, _ = jobs.Show(ctx, queued.Request.ID)
	if job.Status != "cancelled" {
		t.Fatalf("retirement left specialist pending: %+v", job)
	}
	rec, _ = call(t, h, http.MethodGet, url+"/requests/"+r.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("archived specialist result: %d", rec.Code)
	}
	rec, _ = call(t, h, http.MethodPost, url+"/specialists", map[string]any{"name": "late-reviewer", "purpose": role})
	if rec.Code != http.StatusConflict {
		t.Fatalf("created specialist after retirement: %d", rec.Code)
	}
}

// Runs the same literal verifier command that Tend owns without changing a
// completed job's status merely to test its request-specific completion check.
func (a *application) jobsResolveForTest(ctx context.Context, id string) (int, []byte, []byte, error) {
	job, err := a.jobs.Show(ctx, id)
	if err != nil {
		return -1, nil, nil, err
	}
	stdout, stderr, code, err := runCommand(ctx, job.CheckArgv[0], job.CheckArgv[1:], job.Cwd, nil, os.Environ(), 30*time.Second)
	return code, stdout, stderr, err
}

func TestSpecialistAdmissionRejectsInvalidHomesAndPlanning(t *testing.T) {
	a, _, _ := newTestApp(t)
	ctx := context.Background()
	w, _, err := a.createWorker(ctx, createWorkerRequest{Name: "Reporter", Purpose: "Write reports."})
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []intakeRequest{
		{Text: "task", Specialist: "../escape"},
		{Text: "task", Specialist: "missing"},
		{Text: "task", Specialist: "reviewer", Plan: true},
	} {
		if _, err := a.intake(ctx, w, in); err == nil {
			t.Fatalf("accepted %+v", in)
		}
	}
	root := filepath.Join(a.homeDir(w.Slug), "agents")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.intake(ctx, w, intakeRequest{Text: "task", Specialist: "linked"}); err == nil {
		t.Fatal("symlink specialist accepted")
	}
	requests, _ := a.store.Requests(w.Slug)
	if len(requests) != 0 {
		t.Fatal("rejected specialist task was saved")
	}
}

func TestSpecialistCustomCheckEvidence(t *testing.T) {
	a, jobs, _ := newTestApp(t)
	ctx := context.Background()
	w, _, err := a.createWorker(ctx, createWorkerRequest{Name: "Reporter", Purpose: "Write reports."})
	if err != nil {
		t.Fatal(err)
	}
	h := a.routes()
	rec, _ := call(t, h, http.MethodPost, "/api/workers/"+w.Slug+"/specialists", map[string]any{"name": "reviewer", "purpose": "Review evidence."})
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	// Existing Agent homes may have their own executable criteria. Never label
	// those as Hire's generated nonempty-result and structured-check contract.
	custom := "#!/bin/sh\n# Custom local criteria\nexit 0\n"
	if err := os.WriteFile(filepath.Join(a.homeDir(w.Slug), "agents", "reviewer", "bin", "check"), []byte(custom), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := a.intake(ctx, w, intakeRequest{Text: "Review evidence", Specialist: "reviewer", Check: "false"}); err == nil {
		t.Fatal("custom specialist silently ignored a task-specific check")
	}
	result, err := a.intake(ctx, w, intakeRequest{Text: "Review supplied evidence", Specialist: "reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	if code, err := jobs.Work(ctx); code != 0 || err != nil {
		t.Fatalf("work: %d %v", code, err)
	}
	proof := a.taskEvidence(ctx, w, result.Request)
	if proof["standardCheck"] != false {
		t.Fatalf("custom check mislabeled: %+v", proof)
	}
	records, _ := readExecutionEvidence(a.store.workerDir(w.Slug), result.Request.ID)
	if len(records) != 1 || records[0].CheckScript != custom {
		t.Fatalf("custom check not available to read: %+v", records)
	}
}
