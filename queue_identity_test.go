package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// Return the public representation Tend reports, while keeping its underlying
// saved definition available to assert that reconciliation never mutates it.
type reportedQueueJob struct {
	Jobs
	job         Job
	submissions int
}

func (q *reportedQueueJob) Show(context.Context, string) (Job, error) { return q.job, nil }
func (q *reportedQueueJob) Submit(context.Context, string, string, time.Time, []string, []string) error {
	q.submissions++
	return errors.New("an existing job must not be submitted again")
}

func TestReconciliationRecognizesTendCompletionCheck(t *testing.T) {
	for _, status := range []string{"ready", "running", "waiting", "done", "failed", "cancelled", "unknown"} {
		t.Run(status, func(t *testing.T) {
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
			job, err := jobs.Show(ctx, result.Request.ID)
			if err != nil {
				t.Fatal(err)
			}
			originalCheck := slices.Clone(job.CheckArgv)
			job.Status = status
			// Literal expected wire format: tend submit -check COMMAND reports sh -c.
			job.CheckArgv = []string{"/bin/sh", "-c", "exec " + shellQuote(a.executable) + " 'verify' " + shellQuote(a.homeDir(w.Slug)) + " " + shellQuote(a.store.RequestPath(w.Slug, job.ID))}
			reported := &reportedQueueJob{Jobs: jobs, job: job}
			a.jobs = reported
			for range 3 {
				a.runner.Tick(ctx, a.now())
			}
			if got := a.runner.Status().Errors; len(got) > 0 {
				t.Fatalf("%s task was falsely reported as a conflict: %v", status, got)
			}
			if reported.submissions != 0 || reported.job.Status != status {
				t.Fatal("reconciliation resubmitted or changed an existing job")
			}
			stored, _ := jobs.Show(ctx, job.ID)
			if !slices.Equal(stored.CheckArgv, originalCheck) {
				t.Fatal("the recorded completion check was rewritten")
			}
		})
	}
}

func TestReconciliationStillRejectsDifferentJobs(t *testing.T) {
	for _, change := range []string{"working directory", "executable", "task", "check", "shell suffix", "shell flag"} {
		t.Run(change, func(t *testing.T) {
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
			job, _ := jobs.Show(ctx, result.Request.ID)
			job.Argv = slices.Clone(job.Argv)
			check := "exec " + shellQuote(a.executable) + " 'verify' " + shellQuote(a.homeDir(w.Slug)) + " " + shellQuote(a.store.RequestPath(w.Slug, job.ID))
			job.CheckArgv = []string{"/bin/sh", "-c", check}
			switch change {
			case "working directory":
				job.Cwd += "-other"
			case "executable":
				job.Argv[0] += "-other"
			case "task":
				job.Argv[3] += "-other"
			case "check":
				job.CheckArgv[2] = strings.Replace(check, "'verify'", "'exec'", 1)
			case "shell suffix":
				job.CheckArgv[2] += "; echo unexpected"
			case "shell flag":
				job.CheckArgv[1] = "-lc"
			}
			reported := &reportedQueueJob{Jobs: jobs, job: job}
			a.jobs = reported
			err = a.submitRequest(ctx, result.Request)
			if err == nil {
				t.Fatalf("accepted a different %s", change)
			}
			if !strings.Contains(err.Error(), job.ID) {
				t.Fatalf("conflict did not identify task %s: %v", job.ID, err)
			}
			if reported.submissions != 0 {
				t.Fatal("tried to overwrite a conflicting job")
			}
		})
	}
}

func TestSubmittedCheckMatchingIsLiteral(t *testing.T) {
	check := []string{"/tmp/hire ' $(must-not-run)", "verify", "/tmp/a home", "/tmp/requests/task.json"}
	wrapped := []string{"/bin/sh", "-c", `exec '/tmp/hire '"'"' $(must-not-run)' 'verify' '/tmp/a home' '/tmp/requests/task.json'`}
	if !matchesSubmittedCheck(wrapped, check) {
		t.Fatal("quoted paths did not match the exact submitted check")
	}
	if !matchesSubmittedCheck(check, check) {
		t.Fatal("direct check argv no longer matches the memory adapter")
	}
	wrapped[2] += " "
	if matchesSubmittedCheck(wrapped, check) {
		t.Fatal("a changed shell command was silently normalized")
	}
}
