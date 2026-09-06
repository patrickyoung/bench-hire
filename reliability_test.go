package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type failingSubmission struct {
	Jobs
	fail bool
}

func (j *failingSubmission) Submit(ctx context.Context, id, cwd string, at time.Time, argv, check []string) error {
	if j.fail {
		return errors.New("injected Tend submission failure")
	}
	return j.Jobs.Submit(ctx, id, cwd, at, argv, check)
}

func TestSavedWorkRecoversSubmissionFailure(t *testing.T) {
	a, jobs, _ := newTestApp(t)
	ctx := context.Background()
	w, _, err := a.createWorker(ctx, createWorkerRequest{Name: "Daily writer", Purpose: "Write a daily note."})
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.buildRoutine(w.Slug, routineInput{Title: "Daily note", Instructions: "Write a note", Every: "daily", At: "09:00"}, "user", a.now())
	if err != nil {
		t.Fatal(err)
	}
	r.NextDue = a.now()
	if err := a.store.SaveRoutine(r); err != nil {
		t.Fatal(err)
	}
	fault := &failingSubmission{Jobs: jobs, fail: true}
	a.jobs = fault
	if got := a.runner.Tick(ctx, a.now()); got != 0 {
		t.Fatalf("failed tick reported %d queued", got)
	}
	fault.fail = false
	a.runner.Tick(ctx, a.now())
	list, err := jobs.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("recovery produced %d jobs, want one", len(list))
	}
	id := list[0].ID
	jobs.MarkUnknown(id)
	a.runner.Tick(ctx, a.now())
	list, _ = jobs.List(ctx)
	if len(list) != 1 || list[0].Status != "unknown" {
		t.Fatalf("reconciliation repeated uncertain work: %+v", list)
	}

	// Ordinary inbox requests use the same durable reconciliation path.
	fault.fail = true
	accepted, err := a.intake(ctx, w, intakeRequest{Text: "An ordinary task"})
	if err != nil || len(accepted.Warnings) != 1 {
		t.Fatalf("saved task should be acknowledged with a queue warning: %+v %v", accepted, err)
	}
	fault.fail = false
	a.runner.Tick(ctx, a.now())
	list, _ = jobs.List(ctx)
	if len(list) != 2 {
		t.Fatalf("inbox request was lost: %+v", list)
	}
}

func TestUnknownResolutionChecksExactRequest(t *testing.T) {
	a, jobs, _ := newTestApp(t)
	ctx := context.Background()
	w, _, err := a.createWorker(ctx, createWorkerRequest{Name: "Writer", Purpose: "Write notes."})
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.intake(ctx, w, intakeRequest{Text: "Write a note"})
	if err != nil {
		t.Fatal(err)
	}
	id := result.Request.ID
	if _, err := jobs.Work(ctx); err != nil {
		t.Fatal(err)
	}
	jobs.MarkUnknown(id)
	home := a.homeDir(w.Slug)
	original, err := os.ReadFile(filepath.Join(home, "REQUEST.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "REQUEST.md"), []byte("id: another-task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Resolve(ctx, id, "done"); err == nil {
		t.Fatal("another request's result was accepted")
	}
	if err := os.WriteFile(filepath.Join(home, "REQUEST.md"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Resolve(ctx, id, "done"); err != nil {
		t.Fatal(err)
	}
	job, _ := jobs.Show(ctx, id)
	if job.Status != "done" {
		t.Fatalf("got %s", job.Status)
	}

	if err := jobs.Submit(ctx, "legacy", home, a.now(), []string{"/bin/true"}, nil); err != nil {
		t.Fatal(err)
	}
	jobs.MarkUnknown("legacy")
	if err := jobs.Resolve(ctx, "legacy", "done"); err == nil || !strings.Contains(err.Error(), "no submitted -check") {
		t.Fatalf("stand-in must refuse an unknown job without a check: %v", err)
	}
}

func TestAcceptedPlanFinishesInstallingAfterInterruption(t *testing.T) {
	a, jobs, _ := newTestApp(t)
	ctx := context.Background()
	w, _, err := a.createWorker(ctx, createWorkerRequest{Name: "Planner", Purpose: "Do independent jobs."})
	if err != nil {
		t.Fatal(err)
	}
	parent := Request{ID: "req-saved-plan", WorkerSlug: w.Slug, Kind: "request", Text: "Do both jobs", CreatedAt: a.now(), Model: w.Model, NotBefore: a.now()}
	one := parent
	one.ID, one.ParentID, one.Runs, one.Text = parent.ID+"-a1", parent.ID, true, "First complete instruction"
	two := one
	two.ID, two.Text = parent.ID+"-a2", "Second complete instruction"
	routine, err := a.buildRoutine(w.Slug, routineInput{Instructions: "Write a daily note", Every: "daily", At: "09:00"}, "plan:"+parent.ID, a.now())
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{RequestID: parent.ID, Actions: []PlannedAction{{Instructions: one.Text, RequestID: one.ID, When: "now"}, {Instructions: two.Text, RequestID: two.ID, When: "now"}}}
	batch := intakeBatch{Schema: "bench-hire-intake/v1", Response: intakeResponse{Request: parent, Plan: &plan, Actions: []Request{one, two}, Routines: []Routine{routine}}}
	if err := writeJSONAtomic(filepath.Join(a.store.workerDir(w.Slug), "inbox", parent.ID+".json"), batch, true); err != nil {
		t.Fatal(err)
	}
	// Simulate process death after just the parent was materialized, followed
	// by a temporary obstacle when installing the remaining records.
	if err := a.store.CreateRequest(parent); err != nil {
		t.Fatal(err)
	}
	blocked := a.store.RequestPath(w.Slug, two.ID)
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	a.runner.Tick(ctx, a.now())
	list, _ := jobs.List(ctx)
	if len(list) != 0 {
		t.Fatal("a partially installed plan started work")
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	a.runner.Tick(ctx, a.now())
	list, _ = jobs.List(ctx)
	if len(list) != 2 {
		t.Fatalf("whole plan did not recover: %+v", list)
	}
	recovered, err := a.store.Request(w.Slug, two.ID)
	if err != nil || recovered.Text != two.Text {
		t.Fatalf("instruction was lost: %+v %v", recovered, err)
	}
	if err := a.store.DeleteRoutine(w.Slug, routine.ID); err != nil {
		t.Fatal(err)
	}
	a.runner.Tick(ctx, a.now())
	routines, err := a.store.Routines(w.Slug)
	if err != nil || len(routines) != 0 {
		t.Fatalf("reconciliation recreated a removed routine: %+v %v", routines, err)
	}
}

// Opt-in composition tests keep the default suite independent of installed
// Bench programs. HIRE_INTEGRATION_BIN_DIR names one real suite for this run.
func TestTendSubmissionAndResolutionContract(t *testing.T) {
	bin := os.Getenv("HIRE_INTEGRATION_BIN_DIR")
	if bin == "" {
		t.Skip("set HIRE_INTEGRATION_BIN_DIR for real Bench composition tests")
	}
	bin, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := newTestApp(t)
	ctx := context.Background()
	j, err := newTendJobs(filepath.Join(bin, "tend"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.jobs = j
	w, _, err := a.createWorker(ctx, createWorkerRequest{Name: "Real queue", Purpose: "Write notes."})
	if err != nil {
		t.Fatal(err)
	}
	// Literal arguments containing shell syntax must survive the check seam.
	home := a.homeDir(w.Slug)
	proof := filepath.Join(home, "proof ' $(must-not-run)")
	check := writeScript(t, t.TempDir(), "check", "#!/bin/sh\n[ \"$1\" = "+shellQuote(proof)+" ] && test -s \"$1\"\n")
	at := time.Now().Add(100 * time.Millisecond).Truncate(time.Microsecond)
	argv := []string{"/bin/sh", "-c", "printf done > " + shellQuote(proof) + "; exit 125"}
	if err := j.Submit(ctx, "real-uncertain", home, at, argv, []string{check, proof}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(at) + 20*time.Millisecond)
	if err := j.Submit(ctx, "real-uncertain", home, at, argv, []string{check, proof}); err != nil {
		t.Fatalf("identical resubmission changed after due time: %v", err)
	}
	if _, err := j.Work(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := j.Show(ctx, "real-uncertain")
	if err != nil || job.Status != "unknown" {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if err := j.Resolve(ctx, job.ID, "done"); err != nil {
		t.Fatal(err)
	}
	job, _ = j.Show(ctx, job.ID)
	if job.Status != "done" {
		t.Fatalf("got %s", job.Status)
	}
	if _, err := j.Check(ctx); err != nil {
		t.Fatal(err)
	}
}
