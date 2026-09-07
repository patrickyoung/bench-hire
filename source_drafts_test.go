package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func awaitSourceDraft(t *testing.T, a *application, id, slug string) SourceDraft {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		rec, _ := call(t, a.routes(), "GET", "/api/source-drafts/"+id+"?worker="+slug, nil)
		var result struct {
			Draft SourceDraft `json:"draft"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &result) != nil {
			t.Fatalf("read draft: %s", rec.Body.String())
		}
		if result.Draft.State != "drafting" {
			return result.Draft
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("source draft did not finish")
	return SourceDraft{}
}

func startTestSourceDraft(t *testing.T, a *application, slug, kind string, ids []string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	rec, payload := call(t, a.routes(), "POST", "/api/source-drafts", map[string]any{"workerSlug": slug, "kind": kind, "uploadIDs": ids, "input": map[string]string{"content": "Help me use these materials.", "network": "true", "check": "do not pass shell commands"}})
	id := ""
	if draft, ok := payload["draft"].(map[string]any); ok {
		id, _ = draft["id"].(string)
	}
	return rec, id
}

func TestSourceDraftsInterpretEveryPurposeWithoutApplying(t *testing.T) {
	a, _, _ := newTestApp(t)
	configureSourceFixture(t, a)
	a.tools.paths["ask"] = writeScript(t, t.TempDir(), "ask", sourceAskFixture)
	worker, _, err := a.createWorker(context.Background(), createWorkerRequest{Name: "Source manager", Purpose: "Review reports"})
	if err != nil {
		t.Fatal(err)
	}
	home := a.homeDir(worker.Slug)
	oldMemory := "Reports are due on Tuesday. Verify with the manager."
	if err := writeHomeFile(home, "state/kv/report-day.md", oldMemory, true); err != nil {
		t.Fatal(err)
	}
	u := readyUpload(t, a, worker.Slug, "handbook.md", "Include refunds and identify missing records.")
	for _, kind := range []string{"memory", "skill", "task", "routine", "feedback", "job"} {
		t.Run(kind, func(t *testing.T) {
			rec, id := startTestSourceDraft(t, a, worker.Slug, kind, []string{u.ID})
			if rec.Code != 202 {
				t.Fatalf("draft: %s", rec.Body.String())
			}
			d := awaitSourceDraft(t, a, id, worker.Slug)
			if d.State != "ready" {
				t.Fatalf("draft failed: %+v", d)
			}
			var task struct {
				Purpose        string            `json:"purpose"`
				ExistingMemory map[string]string `json:"existingMemory"`
				ManagerDraft   map[string]string `json:"managerDraft"`
			}
			if err := readJSON(d.Session+".task.json", &task); err != nil {
				t.Fatal(err)
			}
			if task.Purpose != kind || len(task.ManagerDraft) != 3 || task.ManagerDraft["content"] == "" {
				t.Fatalf("wrong task context: %+v", task)
			}
			if kind == "memory" {
				if task.ExistingMemory["state/kv/report-day.md"] != oldMemory {
					t.Fatalf("existing memory missing: %+v", task)
				}
				if len(d.Proposal.Memories) != 2 || d.Proposal.Memories[1].Basis != "inferred" {
					t.Fatalf("missing reviewable memory: %+v", d.Proposal)
				}
				for _, m := range d.Proposal.Memories {
					if !strings.Contains(m.Content, u.Ref) {
						t.Fatal("memory lacks its citation")
					}
				}
			} else if !strings.Contains(d.Proposal.Content, u.Ref) {
				t.Fatal("proposal lacks its citation")
			}
			rec, _ = call(t, a.routes(), "GET", "/api/source-drafts/"+id+"?worker=another-worker", nil)
			if rec.Code != 404 {
				t.Fatal("draft crossed worker scope")
			}
		})
	}
	_, list := call(t, a.routes(), "GET", "/api/source-drafts?worker="+worker.Slug+"&kind=memory", nil)
	if len(list["drafts"].([]any)) != 1 {
		t.Fatalf("draft history: %+v", list)
	}
	_, detail := call(t, a.routes(), "GET", "/api/workers/"+worker.Slug, nil)
	if len(detail["requests"].([]any)) != 0 || len(detail["routines"].([]any)) != 0 {
		t.Fatal("drafting assigned work")
	}
	view, err := browseHome(home, "state/kv")
	if err != nil || len(view.Entries) != 1 {
		t.Fatalf("drafting wrote memory: %+v %v", view, err)
	}
	skills, err := os.ReadDir(filepath.Join(home, "skills"))
	if err != nil || len(skills) != 0 {
		t.Fatalf("drafting installed a skill: %v %v", skills, err)
	}
	actual, _ := os.ReadFile(filepath.Join(home, "state/kv/report-day.md"))
	if string(actual) != oldMemory {
		t.Fatal("existing memory was replaced")
	}
}

func TestSourceDraftsRejectUncitedMemoriesAndInvalidInferences(t *testing.T) {
	a, _, _ := newTestApp(t)
	configureSourceFixture(t, a)
	worker, _, err := a.createWorker(context.Background(), createWorkerRequest{Name: "Reviewer", Purpose: "Read records"})
	if err != nil {
		t.Fatal(err)
	}
	u := readyUpload(t, a, worker.Slug, "facts.txt", "Include refunds.")
	for _, test := range []struct{ name, secondContent, basis string }{
		{"one cited item cannot cover a second uncited item", "An unsupported assertion.", "stated"},
		{"unknown reference", "A claim. [ctx:uploads:invented](/sources/missing)", "inferred"},
		{"inference must be labeled", "A claim. [" + u.Ref + "](" + u.URL + ")", "verified"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := SourceProposal{Name: "report-notes", Memories: []SourceMemory{{Topic: "Refunds", Content: "Include refunds. [" + u.Ref + "](" + u.URL + ")", Basis: "stated", Reason: "Review completeness", ReviewAfter: "When policy changes"}, {Topic: "Another claim", Content: test.secondContent, Basis: test.basis, Reason: "A hypothesis", ReviewAfter: "Before use"}}}
			p.Questions = []string{"Review the source owner. [" + u.Ref + "](" + u.URL + ")"}
			raw, _ := json.Marshal(p)
			t.Setenv("FAKE_ASK_REPLY", writeTestReply(t, t.TempDir(), "reply.json", string(raw)))
			rec, id := startTestSourceDraft(t, a, worker.Slug, "memory", []string{u.ID})
			if rec.Code != 202 {
				t.Fatal(rec.Body.String())
			}
			d := awaitSourceDraft(t, a, id, worker.Slug)
			if d.State != "failed" || d.Error == "" || len(d.Proposal.Memories) != 0 {
				t.Fatalf("bad draft available to use: %+v", d)
			}
		})
	}
	// No useful memory is a supported result, not an invitation to invent one.
	raw, _ := json.Marshal(SourceProposal{Name: "no-new-memory"})
	t.Setenv("FAKE_ASK_REPLY", writeTestReply(t, t.TempDir(), "empty.json", string(raw)))
	_, id := startTestSourceDraft(t, a, worker.Slug, "memory", []string{u.ID})
	d := awaitSourceDraft(t, a, id, worker.Slug)
	if d.State != "ready" || d.Proposal.Memories == nil || len(d.Proposal.Memories) != 0 {
		t.Fatalf("empty result: %+v", d)
	}
}

func TestSourceDraftScopeInterruptionAndMemorySave(t *testing.T) {
	a, _, _ := newTestApp(t)
	configureSourceFixture(t, a)
	worker, _, err := a.createWorker(context.Background(), createWorkerRequest{Name: "Memory reader", Purpose: "Read sources"})
	if err != nil {
		t.Fatal(err)
	}
	u := readyUpload(t, a, worker.Slug, "source.txt", "Keep source dates.")
	global := readyUpload(t, a, "", "global.txt", "A shared reference.")
	for _, test := range []struct {
		slug, kind string
		ids        []string
		status     int
	}{
		{"", "memory", []string{global.ID}, 400},
		{"", "job", []string{u.ID}, 409},
		{worker.Slug, "memory", nil, 400},
		{worker.Slug, "memory", []string{u.ID, u.ID}, 409},
		{worker.Slug, "permissions", []string{u.ID}, 400},
	} {
		rec, _ := startTestSourceDraft(t, a, test.slug, test.kind, test.ids)
		if rec.Code != test.status {
			t.Fatalf("%+v: %s", test, rec.Body.String())
		}
	}
	interrupted := SourceDraft{ID: "interrupted-source-draft", WorkerSlug: worker.Slug, Kind: "memory", State: "drafting"}
	path, _ := a.sourceDraftPath(interrupted.ID)
	if err := writeJSONAtomic(path, interrupted, true); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		d := awaitSourceDraft(t, a, interrupted.ID, worker.Slug)
		if d.State != "failed" || !strings.Contains(d.Error, "interrupted") {
			t.Fatalf("restarted an orphan: %+v", d)
		}
	}
	content := "Basis: Inferred; confirm before relying on it.\n\nKeep source dates. [" + u.Ref + "](" + u.URL + ")\n\nCheck again: when reporting policy changes."
	body := map[string]any{"path": "state/kv/source-dates.md", "content": content, "uploadIDs": []string{u.ID}, "createOnly": true}
	// The memory can only become authoritative through the normal reviewed save.
	_ = os.MkdirAll(a.askDir, 0700)
	rec, _ := call(t, a.routes(), "PUT", "/api/workers/"+worker.Slug+"/files", body)
	if rec.Code != 200 {
		t.Fatalf("save: %s", rec.Body.String())
	}
	actual, _ := os.ReadFile(filepath.Join(a.homeDir(worker.Slug), "state/kv/source-dates.md"))
	if !strings.Contains(string(actual), content) || !strings.Contains(string(actual), "inputs/uploads/") {
		t.Fatal("memory lost its qualification or evidence")
	}
	rec, _ = call(t, a.routes(), "PUT", "/api/workers/"+worker.Slug+"/files", body)
	if rec.Code != 409 {
		t.Fatal("memory silently overwrote an existing note")
	}
	body["path"], body["content"] = "state/kv/bad-link.md", "[ctx:uploads:fake](/sources/missing)"
	rec, _ = call(t, a.routes(), "PUT", "/api/workers/"+worker.Slug+"/files", body)
	if rec.Code != 409 {
		t.Fatal("memory accepted a fabricated source link")
	}
	worker.RetiredAt = new(time.Time)
	if err := a.store.SaveWorker(worker); err != nil {
		t.Fatal(err)
	}
	rec, _ = startTestSourceDraft(t, a, worker.Slug, "memory", []string{u.ID})
	if rec.Code != 409 {
		t.Fatal("retired worker accepted new synthesis")
	}
}
