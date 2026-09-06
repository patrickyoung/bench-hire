package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTaskDeliverablesStayWithTheirRequest(t *testing.T) {
	a, _, _ := newTestApp(t)
	ctx := context.Background()
	w, _, err := a.createWorker(ctx, createWorkerRequest{Name: "Writer", Purpose: "Write notes."})
	if err != nil {
		t.Fatal(err)
	}
	task, err := a.intake(ctx, w, intakeRequest{Text: "Write a report with supporting totals."})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(a.homeDir(w.Slug), "work", "requests", task.Request.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(dir, "RESULT.md"):                           "# Actual report\n\nSee totals.tsv.\n",
		filepath.Join(dir, "totals.tsv"):                          "Region\tTotal\nSouth\t170\n",
		filepath.Join(a.homeDir(w.Slug), "work", "unrelated.txt"): "Another task's output",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(a.homeDir(w.Slug), "work", "unrelated.txt"), filepath.Join(dir, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	rec, payload := call(t, a.routes(), http.MethodGet, "/api/workers/writer/requests/"+task.Request.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("task: %d %s", rec.Code, rec.Body.String())
	}
	files, ok := payload["deliverables"].([]any)
	if !ok || len(files) != 1 || files[0].(map[string]any)["path"] != "work/requests/"+task.Request.ID+"/totals.tsv" {
		t.Fatalf("only this task's regular supporting files belong to the delivery: %#v", payload["deliverables"])
	}
}

func TestRetirementStopsALateClaimWithoutStartingAgent(t *testing.T) {
	a, jobs, _ := newTestApp(t)
	ctx := context.Background()
	w, _, err := a.createWorker(ctx, createWorkerRequest{Name: "Writer", Purpose: "Write notes."})
	if err != nil {
		t.Fatal(err)
	}
	request, err := a.intake(ctx, w, intakeRequest{Text: "A late-claimed task"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := jobs.Show(ctx, request.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.retireWorker(ctx, w.Slug, true); err != nil {
		t.Fatal(err)
	}
	_, stderr, code, err := runCommand(ctx, job.Argv[0], job.Argv[1:], job.Cwd, nil, os.Environ(), 30*time.Second)
	if err != nil || code != execRetiredExit || !strings.Contains(string(stderr), "this task did not start") {
		t.Fatalf("late claim: %d %s %v", code, stderr, err)
	}
	if _, err := os.Stat(filepath.Join(a.homeDir(w.Slug), "REQUEST.md")); !os.IsNotExist(err) {
		t.Fatal("late claim wrote a request into the retired home")
	}
	runs, err := readExecutionEvidence(a.store.workerDir(w.Slug), request.Request.ID)
	if err != nil || len(runs) != 0 {
		t.Fatalf("late claim recorded an execution: %+v %v", runs, err)
	}
	job.Status = "failed"
	if state := deriveState(request.Request, &job, &code, a.now()); state != "not-started" {
		t.Fatalf("retirement status: %s", state)
	}
	declined := 3
	if state := deriveState(request.Request, &job, &declined, a.now()); state == "not-started" {
		t.Fatal("Ply's declined approval was mislabeled as a retirement stop")
	}
}

func TestManagerReviewAndRetirement(t *testing.T) {
	a, jobs, _ := newTestApp(t)
	h := a.routes()
	ctx := context.Background()
	w, _, err := a.createWorker(ctx, createWorkerRequest{Name: "Writer", Purpose: "Write notes."})
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.intake(ctx, w, intakeRequest{Text: "Write a useful note"})
	if err != nil {
		t.Fatal(err)
	}
	id := result.Request.ID
	if _, err := jobs.Work(ctx); err != nil {
		t.Fatal(err)
	}
	url := "/api/workers/writer/requests/" + id
	rec, payload := call(t, h, http.MethodGet, url, nil)
	if rec.Code != 200 || payload["request"].(map[string]any)["state"] != "review" {
		t.Fatalf("task=%v", payload)
	}
	request := payload["request"].(map[string]any)
	body := map[string]any{"decision": "accepted", "resultSha256": request["resultSha256"], "jobUpdatedUs": payload["job"].(map[string]any)["updated_us"]}
	rec, _ = call(t, h, http.MethodPost, url+"/review", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	_, payload = call(t, h, http.MethodGet, "/api/workers/writer", nil)
	if payload["accepted"] != float64(1) || payload["firstAcceptedAt"] == nil {
		t.Fatalf("first useful result was not recorded: %v", payload)
	}

	// Acceptance binds the bytes actually read, not just the task ID.
	path := filepath.Join(a.homeDir(w.Slug), "work", "requests", id, "RESULT.md")
	if err := os.WriteFile(path, []byte("A changed result\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, payload = call(t, h, http.MethodGet, url, nil)
	if payload["request"].(map[string]any)["state"] != "review" || payload["request"].(map[string]any)["reviewStale"] != true {
		t.Fatalf("changed result retained acceptance: %v", payload)
	}
	rec, _ = call(t, h, http.MethodPost, url+"/review", body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale acceptance returned %d", rec.Code)
	}

	uncertain, err := a.intake(ctx, w, intakeRequest{Text: "Uncertain work"})
	if err != nil {
		t.Fatal(err)
	}
	jobs.MarkUnknown(uncertain.Request.ID)
	pending, err := a.intake(ctx, w, intakeRequest{Text: "Pending work"})
	if err != nil {
		t.Fatal(err)
	}
	rec, payload = call(t, h, http.MethodDelete, "/api/workers/writer", nil)
	if rec.Code != http.StatusOK || payload["worker"].(map[string]any)["retiringAt"] == nil || payload["worker"].(map[string]any)["retiredAt"] != nil {
		t.Fatalf("retirement must wait: %d %v", rec.Code, payload)
	}
	job, _ := jobs.Show(ctx, pending.Request.ID)
	if job.Status != "cancelled" {
		t.Fatalf("pending job=%s", job.Status)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("retirement moved evidence: %v", err)
	}
	rec, _ = call(t, h, http.MethodPost, "/api/workers/writer/requests", intakeRequest{Text: "Too late"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("retiring worker accepted work: %d", rec.Code)
	}
	rec, _ = call(t, h, http.MethodPost, "/api/workers/writer/requests/"+uncertain.Request.ID+"/resolve", map[string]string{"decision": "fail"})
	if rec.Code != http.StatusOK {
		t.Fatalf("could not resolve retirement blocker: %d %s", rec.Code, rec.Body.String())
	}
	a.runner.Tick(ctx, a.now())
	rec, payload = call(t, h, http.MethodGet, "/api/workers/writer", nil)
	if rec.Code != http.StatusOK || payload["retiredAt"] == nil {
		t.Fatalf("retired worker not accessible: %d %v", rec.Code, payload)
	}
	_, payload = call(t, h, http.MethodGet, "/api/bootstrap", nil)
	if len(payload["workers"].([]any)) != 1 {
		t.Fatal("retired worker disappeared from the archive")
	}
	rec, _ = call(t, h, http.MethodDelete, "/api/workers/writer/files?path=work/requests/"+id+"/RESULT.md", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("retired result could be deleted: %d", rec.Code)
	}
}

func TestFeedbackCreatesOneLinkedRevision(t *testing.T) {
	a, jobs, _ := newTestApp(t)
	ctx := context.Background()
	w, _, err := a.createWorker(ctx, createWorkerRequest{Name: "Editor", Purpose: "Write useful notes."})
	if err != nil {
		t.Fatal(err)
	}
	first, err := a.intake(ctx, w, intakeRequest{Text: "Summarize the supplied results."})
	if err != nil {
		t.Fatal(err)
	}
	if code, err := jobs.Work(ctx); err != nil || code != 0 {
		t.Fatalf("first task: %d %v", code, err)
	}
	h := a.routes()
	url := "/api/workers/editor/requests/" + first.Request.ID
	_, payload := call(t, h, http.MethodGet, url, nil)
	view := payload["request"].(map[string]any)
	feedback := map[string]any{"decision": "changes-requested", "note": "State the source date and explain the discrepancy.", "resultSha256": view["resultSha256"], "jobUpdatedUs": payload["job"].(map[string]any)["updated_us"]}
	rec, payload := call(t, h, http.MethodPost, url+"/review", feedback)
	if rec.Code != http.StatusCreated {
		t.Fatalf("feedback: %d %s", rec.Code, rec.Body.String())
	}
	reviewID := payload["review"].(map[string]any)["id"].(string)
	_, payload = call(t, h, http.MethodPost, url+"/review", feedback)
	if payload["review"].(map[string]any)["id"] != reviewID {
		t.Fatal("repeated feedback created a different receipt")
	}
	resultPath := filepath.Join(a.homeDir(w.Slug), "work", "requests", first.Request.ID, "RESULT.md")
	originalBytes, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultPath, []byte("Changed after review"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, _ = call(t, h, http.MethodPost, url+"/revision", map[string]string{"reviewId": reviewID})
	if rec.Code != http.StatusConflict {
		t.Fatal("stale result was used to create a revision")
	}
	if err := os.WriteFile(resultPath, originalBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	// Simultaneous sends, followed by a failed queue submission and a retry,
	// must all lead to the same saved task and the same eventual Tend job.
	var group sync.WaitGroup
	ids := make(chan string, 6)
	for range 6 {
		group.Go(func() {
			revision, err := a.createRevision(ctx, w.Slug, first.Request.ID, reviewID)
			if err != nil {
				t.Error(err)
			}
			ids <- revision.ID
		})
	}
	group.Wait()
	close(ids)
	id := ""
	for got := range ids {
		if id != "" && id != got {
			t.Fatal("concurrent sends created different revisions")
		}
		id = got
	}
	fault := &failingSubmission{Jobs: jobs, fail: true}
	a.jobs = fault
	rec, payload = call(t, h, http.MethodPost, url+"/revision", map[string]string{"reviewId": reviewID})
	if rec.Code != http.StatusCreated || len(payload["warnings"].([]any)) != 1 {
		t.Fatalf("saved revision not acknowledged: %d %s", rec.Code, rec.Body.String())
	}
	fault.fail = false
	a.runner.Tick(ctx, a.now())
	rec, payload = call(t, h, http.MethodPost, url+"/revision", map[string]string{"reviewId": reviewID})
	if rec.Code != http.StatusCreated || payload["request"].(map[string]any)["id"] != id {
		t.Fatal("retry changed the revision identity")
	}
	requests, err := a.store.Requests(w.Slug)
	if err != nil || len(requests) != 2 {
		t.Fatalf("requests: %d %v", len(requests), err)
	}
	revision, _ := a.store.Request(w.Slug, id)
	if revision.RevisionOf != first.Request.ID || revision.ReviewID != reviewID || !strings.Contains(revision.Text, feedback["note"].(string)) {
		t.Fatal("revision lost its source or feedback")
	}
	_, payload = call(t, h, http.MethodGet, url, nil)
	if payload["request"].(map[string]any)["state"] != "revision-sent" || payload["request"].(map[string]any)["revisionId"] != id {
		t.Fatal("original result does not link to its revision")
	}
	if code, err := jobs.Work(ctx); err != nil || code != 0 {
		t.Fatalf("revision: %d %v", code, err)
	}
	revisionURL := "/api/workers/editor/requests/" + id
	_, payload = call(t, h, http.MethodGet, revisionURL, nil)
	view = payload["request"].(map[string]any)
	rec, _ = call(t, h, http.MethodPost, revisionURL+"/review", map[string]any{"decision": "accepted", "resultSha256": view["resultSha256"], "jobUpdatedUs": payload["job"].(map[string]any)["updated_us"]})
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	_, payload = call(t, h, http.MethodGet, "/api/workers/editor", nil)
	if payload["accepted"] != float64(1) || payload["attention"] != float64(0) {
		t.Fatalf("resolved feedback still needs attention: %v", payload)
	}
	original, _ := a.store.Request(w.Slug, first.Request.ID)
	if original.Text != first.Request.Text {
		t.Fatal("revision overwrote the original task")
	}
	def, err := readAgentDefinition(a.homeDir(w.Slug), w)
	if err != nil || strings.Contains(def.Files.Agents, feedback["note"].(string)) {
		t.Fatal("one task's correction silently changed the worker's standing job")
	}
}

func TestCompleteResultIsRequiredForReview(t *testing.T) {
	a, jobs, _ := newTestApp(t)
	ctx := context.Background()
	w, _, err := a.createWorker(ctx, createWorkerRequest{Name: "Long report", Purpose: "Write detailed reports."})
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.intake(ctx, w, intakeRequest{Text: "Write a report."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Work(ctx); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("Report line.\n", fileReadLimit/10)
	path := filepath.Join(a.homeDir(w.Slug), "work", "requests", result.Request.ID, "RESULT.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	h := a.routes()
	url := "/api/workers/long-report/requests/" + result.Request.ID
	_, preview := call(t, h, http.MethodGet, url, nil)
	if preview["result"].(map[string]any)["truncated"] != true || preview["request"].(map[string]any)["resultSha256"] != nil {
		t.Fatal("partial preview supplied a review digest")
	}
	rec, full := call(t, h, http.MethodGet, url+"?full=1", nil)
	if rec.Code != http.StatusOK || full["result"].(map[string]any)["content"] != content {
		t.Fatal("complete result could not be read")
	}
	digest := full["request"].(map[string]any)["resultSha256"]
	if digest != contentSHA256([]byte(content)) {
		t.Fatal("review did not bind every returned byte")
	}
	rec, _ = call(t, h, http.MethodPost, url+"/review", map[string]any{"decision": "accepted", "resultSha256": digest, "jobUpdatedUs": full["job"].(map[string]any)["updated_us"]})
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
}

func TestSchedulerRechecksEditedAndDeletedRoutines(t *testing.T) {
	a, jobs, _ := newTestApp(t)
	ctx := context.Background()
	w, _, err := a.createWorker(ctx, createWorkerRequest{Name: "Scheduler", Purpose: "Write notes."})
	if err != nil {
		t.Fatal(err)
	}
	routine, err := a.buildRoutine(w.Slug, routineInput{Title: "Note", Instructions: "Write a note", Every: "daily", At: "09:00"}, "user", a.now())
	if err != nil {
		t.Fatal(err)
	}
	routine.NextDue = a.now()
	if err := a.store.SaveRoutine(routine); err != nil {
		t.Fatal(err)
	}
	h := a.routes()
	url := "/api/workers/scheduler/routines/" + routine.ID
	for _, action := range []string{"disable", "delete"} {
		rec, _ := call(t, h, http.MethodPost, url+"/"+action, nil)
		if rec.Code != http.StatusOK {
			t.Fatal(rec.Body.String())
		}
		if queued, err := a.queueScheduledRoutine(ctx, w.Slug, routine.ID, a.now()); err != nil || queued {
			t.Fatalf("stale scheduler snapshot after %s: %v %v", action, queued, err)
		}
	}
	if list, err := jobs.List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("stale routine started work: %v %v", list, err)
	}
	if routines, err := a.store.Routines(w.Slug); err != nil || len(routines) != 0 {
		t.Fatal("scheduler restored a deleted routine")
	}
}
