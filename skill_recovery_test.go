package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSkillInstallRecoveryAtEverySwapPhase(t *testing.T) {
	for _, phase := range []string{"staged", "moved-original", "installed", "metadata", "committed", "external-edit"} {
		t.Run(phase, func(t *testing.T) {
			a, worker, before := newSkillImprovementFixture(t)
			revision := draftTestSkill(t, a, worker, before)
			dir, _ := a.skillImprovementDir(worker.Slug, revision.Skill, revision.ID)
			root, _ := a.skillRoot(worker.Slug, revision.Skill)
			wd := a.store.workerDir(worker.Slug)
			tx := filepath.Join(a.homeDir(worker.Slug), ".agent", "skill-updates", "swap-test")
			if err := os.MkdirAll(tx, 0700); err != nil {
				t.Fatal(err)
			}
			if err := copySkillBundle(filepath.Join(dir, "candidate"), filepath.Join(tx, "incoming"), revision.After); err != nil {
				t.Fatal(err)
			}
			journal := skillTransaction{ID: "swap-test", RevisionID: revision.ID, Skill: revision.Skill, BeforeSHA256: before.SHA256, AfterSHA256: revision.After.SHA256, PriorState: "review", PriorReceipt: worker.Receipt}
			if phase != "staged" {
				if err := os.Rename(root, filepath.Join(tx, "backup")); err != nil {
					t.Fatal(err)
				}
			}
			if phase != "staged" && phase != "moved-original" {
				if err := os.Rename(filepath.Join(tx, "incoming"), root); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "metadata" || phase == "committed" {
				now := a.now()
				revision.State, revision.AppliedAt = "applied", &now
				if err := saveSkillImprovement(dir, revision); err != nil {
					t.Fatal(err)
				}
			}
			journal.Committed = phase == "committed"
			if err := writeJSONAtomic(filepath.Join(wd, "skill-transaction.json"), journal, true); err != nil {
				t.Fatal(err)
			}
			if phase == "external-edit" {
				if err := os.WriteFile(filepath.Join(root, "references", "policy.txt"), []byte("New external policy"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			// A read must recover even when the installed directory is temporarily missing.
			rec, _ := call(t, a.routes(), "GET", skillURL(worker, ""), nil)
			if phase == "external-edit" {
				if rec.Code != 409 {
					t.Fatal("recovery overwrote an external edit")
				}
				raw, _ := os.ReadFile(filepath.Join(root, "references", "policy.txt"))
				if string(raw) != "New external policy" {
					t.Fatal("external policy lost")
				}
				if _, err := os.Stat(filepath.Join(wd, "skill-transaction.json")); err != nil {
					t.Fatal("unresolved journal removed")
				}
				return
			}
			if rec.Code != 200 {
				t.Fatal(rec.Body.String())
			}
			expected := before
			state := "review"
			if journal.Committed {
				expected = revision.After
				state = "applied"
			}
			if err := verifySkillBundle(root, expected); err != nil {
				t.Fatal(err)
			}
			saved := awaitSkillImprovement(t, a, worker, revision.ID)
			if saved.State != state {
				t.Fatalf("state=%s want %s", saved.State, state)
			}
			if _, err := os.Stat(filepath.Join(wd, "skill-transaction.json")); !os.IsNotExist(err) {
				t.Fatal("journal not resolved")
			}
			if err := recoverSkillTransaction(wd, a.homeDir(worker.Slug)); err != nil {
				t.Fatal("repeated recovery:", err)
			}
		})
	}
}

func TestSkillApplyRejectsActiveWorkAndRollsBackFailedValidation(t *testing.T) {
	a, worker, before := newSkillImprovementFixture(t)
	revision := testSkillRevision(t, a, worker, draftTestSkill(t, a, worker, before))
	body := map[string]string{"sha256": revision.ProposalSHA256, "currentSha256": before.SHA256}
	lock, err := lockExecution(a.store.workerDir(worker.Slug), false)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := call(t, a.routes(), "POST", skillURL(worker, revision.ID)+"/apply", body)
	_ = lock.Close()
	if rec.Code != 409 {
		t.Fatal("installed while worker was active")
	}
	// Agent validates the new home after the directory swap. Rejection must
	// restore every old byte and permission and leave the revision reviewable.
	a.tools.paths["agent"] = writeScript(t, t.TempDir(), "agent", "#!/bin/sh\necho 'invalid home' >&2\nexit 1\n")
	rec, _ = call(t, a.routes(), "POST", skillURL(worker, revision.ID)+"/apply", body)
	if rec.Code != 409 {
		t.Fatal("invalid home installed")
	}
	root, _ := a.skillRoot(worker.Slug, revision.Skill)
	if err := verifySkillBundle(root, before); err != nil {
		t.Fatal(err)
	}
	if got := awaitSkillImprovement(t, a, worker, revision.ID); got.State != "review" || !strings.Contains(got.Error, "rolled back") {
		t.Fatalf("lost recovery state: %+v", got)
	}
	a.tools.paths["agent"] = writeScript(t, t.TempDir(), "agent", fakeAgent)
	rec, _ = call(t, a.routes(), "POST", skillURL(worker, revision.ID)+"/apply", body)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	rec, _ = call(t, a.routes(), "POST", skillURL(worker, revision.ID)+"/apply", body)
	if rec.Code != 200 {
		t.Fatal("connection-loss retry did not return installed version:", rec.Body.String())
	}
	if err := os.WriteFile(filepath.Join(root, "references", "policy.txt"), []byte("Later policy"), 0644); err != nil {
		t.Fatal(err)
	}
	rec, _ = call(t, a.routes(), "POST", skillURL(worker, revision.ID)+"/revert", map[string]string{"sha256": revision.ProposalSHA256, "currentSha256": revision.After.SHA256})
	if rec.Code != 409 {
		t.Fatal("rollback overwrote a later policy")
	}
}

func TestSkillReviewSourcesAreIdentifiedByCitationAndFilesByHash(t *testing.T) {
	a, worker, before := newSkillImprovementFixture(t)
	revision := draftTestSkill(t, a, worker, before)
	dir, _ := a.skillImprovementDir(worker.Slug, revision.Skill, revision.ID)
	raw, err := a.skillEvidence(dir, revision)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	raw = bytes.Join(lines, []byte{'\n'})
	revision.EvidenceSHA256 = contentSHA256(raw)
	if err := writeFileAtomic(filepath.Join(dir, "evidence.jsonl"), raw, false); err != nil {
		t.Fatal(err)
	}
	if err := saveSkillImprovement(dir, revision); err != nil {
		t.Fatal(err)
	}
	rec, data := call(t, a.routes(), "GET", skillURL(worker, revision.ID)+"/file?source=0", nil)
	if rec.Code != 200 || data["source"].(map[string]any)["title"] != "Manager's improvement request" {
		t.Fatal(rec.Body.String())
	}
	rec, _ = call(t, a.routes(), "GET", skillURL(worker, revision.ID)+"/file?version=candidate&path=scripts/total.py", nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "candidate", "scripts", "total.py"), []byte("print('tampered')"), 0755); err != nil {
		t.Fatal(err)
	}
	rec, _ = call(t, a.routes(), "GET", skillURL(worker, revision.ID)+"/file?version=candidate&path=scripts/total.py", nil)
	if rec.Code != 409 {
		t.Fatal("review displayed unrecorded file bytes")
	}
}

func TestSkillEditedTestInvalidatesPassingReceipt(t *testing.T) {
	a, worker, before := newSkillImprovementFixture(t)
	revision := testSkillRevision(t, a, worker, draftTestSkill(t, a, worker, before))
	rec, _ := call(t, a.routes(), "PUT", skillURL(worker, revision.ID)+"/file", map[string]string{"sha256": revision.ProposalSHA256, "version": "checks", "path": "totals.py", "content": revision.Plan.CheckFiles[0].Content + "\nassert False, 'A newly required condition fails'\n"})
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	edited := awaitSkillImprovement(t, a, worker, revision.ID)
	if edited.TestedSHA256 != "" || edited.ChecksSHA256 == revision.ChecksSHA256 || edited.After.SHA256 != revision.After.SHA256 {
		t.Fatal("test edit not bound to a new review")
	}
	edited = testSkillRevision(t, a, worker, edited)
	if skillTestsPassed(edited) == nil {
		t.Fatal("new failing test did not block application")
	}
}

func TestRealSuiteSkillImprovement(t *testing.T) {
	bin := os.Getenv("HIRE_INTEGRATION_BIN_DIR")
	if bin == "" {
		t.Skip("set HIRE_INTEGRATION_BIN_DIR to validate real Agent, Brief, Context, Cite and Cage")
	}
	bin, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := newTestApp(t)
	a.tools.paths["agent"] = filepath.Join(bin, "agent")
	worker, before := configureSkillImprovementFixture(t, a)
	for _, name := range []string{"agent", "brief", "context", "cite", "cage"} {
		a.tools.paths[name] = filepath.Join(bin, name)
	}
	a.tools.binDir = bin
	upload := readyUpload(t, a, worker.Slug, "policy.md", "Refunds reduce sales; preserve positive sale totals.")
	revision := testSkillRevision(t, a, worker, draftTestSkill(t, a, worker, before, upload.ID))
	if err := skillTestsPassed(revision); err != nil {
		t.Fatalf("real checks: %v %+v", err, revision.Results)
	}
	rec, _ := call(t, a.routes(), "POST", skillURL(worker, revision.ID)+"/apply", map[string]string{"sha256": revision.ProposalSHA256, "currentSha256": before.SHA256})
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	dir, _ := a.skillImprovementDir(worker.Slug, revision.Skill, revision.ID)
	// A check may write only its disposable copy and private TMPDIR, even when
	// the evidence/homes are themselves under /tmp. No fallback is permitted.
	protected := filepath.Join(a.homeDir(worker.Slug), "work", "outside-trial.txt")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	code := "import pathlib,socket,sys,errno\np=pathlib.Path(sys.argv[1])\ntry:\n p.write_text('outside')\nexcept OSError as e:\n assert e.errno in (errno.EACCES,errno.EPERM,errno.EROFS),e\nelse:\n raise AssertionError('write escaped confinement')\ns=socket.socket()\ns.settimeout(2)\ntry:\n s.connect(('127.0.0.1',int(sys.argv[2])))\nexcept OSError as e:\n assert e.errno in (errno.EACCES,errno.EPERM,errno.ECONNREFUSED,errno.ENETUNREACH),e\nelse:\n raise AssertionError('network not denied')\nprint('Outside writes and network denied.')\n"
	result := a.runSkillCheck(context.Background(), revision, dir, "candidate", SkillCheck{Name: "Confinement", Argv: []string{"python3", "-c", code, protected, strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)}})
	if result.State != "passed" {
		t.Fatalf("confinement: %+v", result)
	}
	if _, err := os.Stat(protected); !os.IsNotExist(err) {
		t.Fatal("check wrote outside its trial")
	}
	// Proposals and results are ordinary persisted records; they remain readable.
	var saved SkillImprovement
	raw, err := os.ReadFile(filepath.Join(dir, "revision.json"))
	if err != nil || json.Unmarshal(raw, &saved) != nil || saved.State != "applied" {
		t.Fatal("missing installed receipt")
	}
}

func TestSkillWorkEvidenceKeepsReviewsOfTheSelectedResult(t *testing.T) {
	a, worker, before := newSkillImprovementFixture(t)
	upload := readyUpload(t, a, worker.Slug, "work-source.md", "The original policy says to subtract refunds.")
	rec, data := call(t, a.routes(), "POST", "/api/workers/"+worker.Slug+"/requests", intakeRequest{Text: "Review the refund report.", UploadIDs: []string{upload.ID}})
	if rec.Code != 201 {
		t.Fatal(rec.Body.String())
	}
	id := data["request"].(map[string]any)["id"].(string)
	result := []byte("# Review\nRefunds were added instead of subtracted.\n")
	if err := writeFileAtomic(filepath.Join(a.homeDir(worker.Slug), "work", "requests", id, "RESULT.md"), result, true); err != nil {
		t.Fatal(err)
	}
	for _, review := range []ResultReview{{ID: "matching-review", RequestID: id, Decision: "feedback", Note: "Please preserve the ordinary sales calculation.", ResultSHA256: contentSHA256(result)}, {ID: "obsolete-review", RequestID: id, Decision: "feedback", Note: "OBSOLETE FEEDBACK MUST NOT APPEAR", ResultSHA256: contentSHA256([]byte("older result"))}} {
		if err := a.store.SaveResultReview(worker.Slug, &review); err != nil {
			t.Fatal(err)
		}
	}
	rec, data = call(t, a.routes(), "POST", skillURL(worker, "")+"/improvements", map[string]any{"goal": "Correct refunds without losing ordinary sales.", "baseSha256": before.SHA256, "requestIDs": []string{id}})
	if rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	revision := awaitSkillImprovement(t, a, worker, data["improvement"].(map[string]any)["id"].(string))
	if revision.State != "review" {
		t.Fatal(revision.Error)
	}
	dir, _ := a.skillImprovementDir(worker.Slug, revision.Skill, revision.ID)
	evidence, err := a.skillEvidence(dir, revision)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Refunds were added instead of subtracted", "preserve the ordinary sales", "jobStatus", "not automatically verified training evidence", "The original policy says to subtract refunds", upload.Ref} {
		if !strings.Contains(string(evidence), want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(string(evidence), "OBSOLETE FEEDBACK") {
		t.Fatal("feedback on other result bytes entered this proposal")
	}
	neighbor, _, err := a.createWorker(context.Background(), createWorkerRequest{Name: "Neighbor", Purpose: "Other reports"})
	if err != nil {
		t.Fatal(err)
	}
	rec, _ = call(t, a.routes(), "GET", skillURL(neighbor, revision.ID), nil)
	if rec.Code != 404 {
		t.Fatal("another worker exposed this review")
	}
}
