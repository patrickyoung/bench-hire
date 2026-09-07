package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func connectedWorkerFixture(t *testing.T, a *application) Worker {
	t.Helper()
	configureSourceFixture(t, a)
	configureConnectedFixture(t, a)
	a.tools.paths["ask"] = writeScript(t, t.TempDir(), "ask", sourceAskFixture)
	worker, _, err := a.createWorker(context.Background(), createWorkerRequest{Name: "CRM assistant", Purpose: "Keep customer records accurate"})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}
func connectedGrantFixture(t *testing.T, a *application, worker Worker, c ConnectedApp, mode string) AppGrant {
	t.Helper()
	catalog, err := a.appCatalogue(c)
	if err != nil {
		t.Fatal(err)
	}
	previous, _ := readAppGrant(a.homeDir(worker.Slug), c.ID)
	var permissions []AppPermission
	for _, cap := range catalog.Capabilities {
		if cap.Kind == "tools" && cap.Name == "find_contact" {
			permissions = append(permissions, AppPermission{Kind: cap.Kind, Name: cap.Name, Digest: cap.Digest, Mode: mode})
		}
	}
	rec, _ := call(t, a.routes(), "PUT", "/api/workers/"+worker.Slug+"/connections/"+c.ID, map[string]any{"catalogueSha256": catalog.SHA256, "version": previous.Version, "permissions": permissions, "instructions": "Check the exact email and retain the supporting record."})
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	grant, err := readAppGrant(a.homeDir(worker.Slug), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	return grant
}
func connectedSessionFixture(t *testing.T, a *application, worker Worker, id string) {
	t.Helper()
	home := a.homeDir(worker.Slug)
	session := filepath.Join(home, ".agent", "runs", id+".jsonl")
	// The offline Action fixture does not read a session format. Real session
	// creation, append and replay are covered by the opt-in public CLI test.
	if err := writeFileAtomic(session, []byte("offline session fixture\n"), false); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(filepath.Join(home, ".agent", "checkpoints", id+".current"), []byte(session+"\n"), false); err != nil {
		t.Fatal(err)
	}
}

func TestConnectedPermissionsCallsSourcesAndResolution(t *testing.T) {
	a, _, _ := newTestApp(t)
	worker := connectedWorkerFixture(t, a)
	c := createConnectedFixture(t, a)
	grant := connectedGrantFixture(t, a, worker, c, "automatic")
	connectedSessionFixture(t, a, worker, "task-one")
	in := appCallRequest{ConnectionID: c.ID, Operation: "run", Name: "find_contact", Input: json.RawMessage(`{"email":"ada@example.test"}`)}
	response := a.executeAppCall(context.Background(), worker, "task-one", in)
	if response.Exit != 0 || response.Call == nil || response.Call.Source == nil {
		t.Fatalf("call: %+v %+v", response, response.Call)
	}
	first := response.Call.ID
	if response.Call.Source.Ref == "" || response.Call.Source.EvidenceSHA256 == "" {
		t.Fatal("no retained Context identity")
	}
	sources, err := a.appTaskSources(worker.Slug, "task-one")
	if err != nil || len(sources) != 1 {
		t.Fatal(sources, err)
	}
	if err := verifyUploadRefs(a.homeDir(worker.Slug), sources); err != nil {
		t.Fatal(err)
	}
	response = a.executeAppCall(context.Background(), worker, "task-one", in)
	if response.Call == nil || response.Call.ID != first {
		t.Fatal("identical operation repeated")
	}
	in.Name = "create_contact"
	response = a.executeAppCall(context.Background(), worker, "task-one", in)
	if response.Exit != 2 || response.Call != nil {
		t.Fatal("ungranted capability ran")
	}
	// Server readOnlyHint is irrelevant to the manager's explicit review mode.
	grant = connectedGrantFixture(t, a, worker, c, "review")
	in.Name = "find_contact"
	in.Input = json.RawMessage(`{"email":"review@example.test"}`)
	response = a.executeAppCall(context.Background(), worker, "task-one", in)
	if response.Exit != 75 || response.Call == nil || response.Call.State != "review" || response.Call.Result != nil {
		t.Fatal("review permission released an operation", response)
	}
	grant = connectedGrantFixture(t, a, worker, c, "automatic")
	in.Input = json.RawMessage(`{"email":"uncertain@example.test"}`)
	response = a.executeAppCall(context.Background(), worker, "task-one", in)
	if response.Exit != 125 || response.Call.State != "uncertain" {
		t.Fatal("unknown outcome lost")
	}
	uncertainID := response.Call.ID
	in.Input = json.RawMessage(`{"email":"new@example.test"}`)
	response = a.executeAppCall(context.Background(), worker, "task-one", in)
	if response.Exit != 125 || response.Call.ID != uncertainID {
		t.Fatal("uncertainty did not stop further app use")
	}
	rec, _ := call(t, a.routes(), "POST", "/api/workers/"+worker.Slug+"/app-calls/"+uncertainID+"/resolve", map[string]string{"resolution": "confirmed-no-effect", "note": "Checked the service audit: no request was processed."})
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	response = a.executeAppCall(context.Background(), worker, "task-one", in)
	if response.Exit != 0 {
		t.Fatal(response.Error)
	}
	box, _ := a.appGrantBox(grant)
	path := filepath.Join(box, "actions", "find_contact")
	raw, _ := os.ReadFile(path)
	_ = os.WriteFile(path, append(raw, []byte("# changed\n")...), 0700)
	in.Input = json.RawMessage(`{"email":"changed@example.test"}`)
	response = a.executeAppCall(context.Background(), worker, "task-one", in)
	if response.Exit != 2 || !strings.Contains(response.Error, "programs changed") {
		t.Fatal("changed admitted programs ran", response)
	}
}

func TestConnectedTeachingUsesGrantedCapabilitiesAndRejectsChangedAccess(t *testing.T) {
	a, _, _ := newTestApp(t)
	worker := connectedWorkerFixture(t, a)
	c := createConnectedFixture(t, a)
	grant := connectedGrantFixture(t, a, worker, c, "automatic")
	rec, data := call(t, a.routes(), "POST", "/api/workers/"+worker.Slug+"/skills/drafts", map[string]any{"connectionId": c.ID, "goal": "Find customers accurately and explain missing records."})
	if rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	id := data["draft"].(map[string]any)["id"].(string)
	var draft SourceSkillDraft
	path, _ := a.sourceSkillPath(worker.Slug, id)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		a.sourceSkillMu.Lock()
		active := a.sourceSkillActive[id]
		_ = readJSON(path, &draft)
		a.sourceSkillMu.Unlock()
		if !active {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if draft.State != "ready" || draft.ConnectionID != c.ID || draft.GrantVersion != grant.Version {
		t.Fatalf("teaching: %+v", draft)
	}
	u, raw, _, err := a.checkedUpload(draft.UploadIDs[0], worker.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if u.Origin != "mcp-capabilities" || !strings.Contains(string(raw), "find_contact") || strings.Contains(string(raw), "create_contact") || strings.Contains(string(raw), "service-secret") {
		t.Fatal("teaching source exceeded grant or exposed credentials", string(raw))
	}
	dataBytes, _ := os.ReadFile(path)
	install := map[string]any{"name": draft.Name, "description": draft.Description, "method": draft.Method, "uploadIDs": draft.UploadIDs, "draftID": draft.ID, "draftSha256": contentSHA256(dataBytes)}
	connectedGrantFixture(t, a, worker, c, "review")
	rec, _ = call(t, a.routes(), "POST", "/api/workers/"+worker.Slug+"/skills", install)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "app access changed") {
		t.Fatal("stale skill draft installed", rec.Body.String())
	}
	current, _ := readAppGrant(a.homeDir(worker.Slug), c.ID)
	if current.Permissions[0].Mode != "review" {
		t.Fatal("skill changed permissions")
	}
}

func TestConnectedRedactionPreservesExactNumbers(t *testing.T) {
	a, _, _ := newTestApp(t)
	configureConnectedFixture(t, a)
	c := createConnectedFixture(t, a)
	raw := a.redactAppData(c, []byte(`{"record":9223372036854775807,"message":"service-secret"}`))
	if !strings.Contains(string(raw), "9223372036854775807") || strings.Contains(string(raw), "service-secret") {
		t.Fatal("redaction changed service data or leaked credentials", string(raw))
	}
}
