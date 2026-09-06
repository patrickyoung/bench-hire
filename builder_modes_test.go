package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureWorkerProposal = `{"message":"Ready for your review.","ready":true,"changes":[],"definition":{"name":"Notes","purpose":"Write useful notes from supplied records.","network":false,"files":{"goal":"# Job\nWrite useful notes from the supplied records.\n","agents":"# Method\nRead REQUEST.md. Use supplied records, identify missing information, and write work/requests/{request_id}/RESULT.md.\n","soul":"","plan":"","memory":"","heartbeat":""},"checks":[]}}`

func configureBuilderFixture(t *testing.T, a *application, proved bool) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FAKE_ASK_REPLY", writeTestReply(t, dir, "proof.txt", "ok\n"))
	t.Setenv("FAKE_BUILDER_SYNTHESIS_REPLY", writeTestReply(t, dir, "proposal.json", fixtureWorkerProposal))
	t.Setenv("FAKE_BUILDER_ROUTER_REPLY", writeTestReply(t, dir, "router.json", `{"experts":[{"name":"Editor","focus":"Check source use and clarity.","reason":"The work summarizes records."}]}`))
	t.Setenv("FAKE_BUILDER_EXPERT_REPLY", writeTestReply(t, dir, "review.json", `{"summary":"The definition is bounded.","recommendations":[],"risks":[]}`))
	log := filepath.Join(dir, "calls.log")
	t.Setenv("FAKE_ASK_LOG", log)
	if proved {
		if err := a.store.SaveModelProof(ModelProof{Model: a.model, OK: true, Output: "ok", At: a.now()}); err != nil {
			t.Fatal(err)
		}
	}
	return log
}

// Both methods exercise identical validation, application and result review.
// The fixture establishes calls and workflow, not comparative model quality.
func TestBuilderModesAndRecordedEffort(t *testing.T) {
	var definitionSHA string
	for _, test := range []struct {
		name, mode string
		proved     bool
		calls      int64
	}{
		{"default", "", true, 1},
		{"default-with-connection-test", "", false, 2},
		{"independent-reviews", "review-team", true, 6},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, jobs, _ := newTestApp(t)
			logPath := configureBuilderFixture(t, a, test.proved)
			h := a.routes()
			body := map[string]string{"message": "Draft a worker that writes notes from supplied records."}
			if test.mode != "" {
				body["mode"] = test.mode
			}
			rec, initial := call(t, h, http.MethodPost, "/api/builder/chat", body)
			if rec.Code != http.StatusAccepted {
				t.Fatal(rec.Body.String())
			}
			if test.mode == "" && latestBuilderTurn(t, initial)["status"] != "drafting" {
				t.Fatal("ordinary draft did not start directly")
			}
			waitForBuilderTurn(t, h, "")
			session, err := a.store.BuilderSession("")
			if err != nil || !session.Ready || session.Proposal == nil {
				t.Fatalf("draft failed: %+v %v", session, err)
			}
			turn := session.Turns[0]
			if turn.Effort == nil || turn.Effort.AskCalls != test.calls || turn.Effort.ElapsedMS < 0 {
				t.Fatalf("effort: %+v, want %d calls", turn.Effort, test.calls)
			}
			log, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if test.mode == "" && (len(turn.Reports) != 0 || turn.RouterSession != "" || strings.Contains(string(log), "builder-router-schema.json") || strings.Contains(string(log), "builder-expert-schema.json")) {
				t.Fatal("default draft secretly started reviewers")
			}
			if test.mode != "" && len(turn.Reports) != 4 {
				t.Fatal("explicit independent reviews were not performed")
			}
			if definitionSHA == "" {
				definitionSHA = agentDefinitionSHA256(*session.Proposal)
			} else if agentDefinitionSHA256(*session.Proposal) != definitionSHA {
				t.Fatal("the two methods applied different definition normalization")
			}
			if workers, err := a.store.Workers(); err != nil || len(workers) != 0 {
				t.Fatal("drafting hired the worker without applying")
			}
			w, err := a.applyBuilderSession(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			result, err := a.intake(context.Background(), w, intakeRequest{Text: "Summarize the supplied record."})
			if err != nil {
				t.Fatal(err)
			}
			if code, err := jobs.Work(context.Background()); err != nil || code != 0 {
				t.Fatalf("first task: %d %v", code, err)
			}
			url := "/api/workers/notes/requests/" + result.Request.ID
			_, resultView := call(t, h, http.MethodGet, url, nil)
			rec, _ = call(t, h, http.MethodPost, url+"/review", map[string]any{"decision": "accepted", "resultSha256": resultView["request"].(map[string]any)["resultSha256"], "jobUpdatedUs": resultView["job"].(map[string]any)["updated_us"]})
			if rec.Code != http.StatusCreated {
				t.Fatal(rec.Body.String())
			}
			_, definition := call(t, h, http.MethodGet, "/api/workers/notes/definition", nil)
			records := definition["drafting"].([]any)
			if len(records) != 1 || records[0].(map[string]any)["effort"].(map[string]any)["askCalls"] != float64(test.calls) {
				t.Fatal("applied definition lost its drafting effort")
			}
			_, worker := call(t, h, http.MethodGet, "/api/workers/notes", nil)
			if worker["accepted"] != float64(1) {
				t.Fatal("first accepted result was not recorded")
			}
			t.Logf("method=%s calls=%d elapsed_ms=%d followups=0 accepted_fixture_results=1", turn.Mode, turn.Effort.AskCalls, turn.Effort.ElapsedMS)
		})
	}
}

func TestSingleDraftFailureKeepsPreviousProposalAndRecordsCost(t *testing.T) {
	a, _, _ := newTestApp(t)
	configureBuilderFixture(t, a, true)
	h := a.routes()
	call(t, h, http.MethodPost, "/api/builder/chat", map[string]string{"message": "Draft the notes worker."})
	waitForBuilderTurn(t, h, "")
	before, err := a.store.BuilderSession("")
	if err != nil || before.Proposal == nil {
		t.Fatal("initial draft missing")
	}
	t.Setenv("FAKE_BUILDER_SYNTHESIS_REPLY", writeTestReply(t, t.TempDir(), "bad.json", "not a proposal"))
	call(t, h, http.MethodPost, "/api/builder/chat", map[string]string{"message": "Make it clearer."})
	waitForBuilderTurn(t, h, "")
	after, err := a.store.BuilderSession("")
	if err != nil || after.Proposal == nil || agentDefinitionSHA256(*after.Proposal) != agentDefinitionSHA256(*before.Proposal) {
		t.Fatal("failed author replaced the last usable proposal")
	}
	last := after.Turns[len(after.Turns)-1]
	if last.Status != "failed" || last.Effort == nil || last.Effort.AskCalls != 1 {
		t.Fatalf("failed draft effort: %+v", last)
	}
	rec, _ := call(t, h, http.MethodPost, "/api/builder/chat", map[string]string{"message": "Try again.", "mode": "unbounded-team"})
	if rec.Code != http.StatusBadRequest {
		t.Fatal("unknown drafting mode accepted")
	}
	legacy := draftingRecords([]BuilderSession{{Turns: []BuilderTurn{{Number: 1, Status: "complete"}}}})
	if legacy[0].Effort != nil {
		t.Fatal("legacy drafting effort was invented")
	}
}
