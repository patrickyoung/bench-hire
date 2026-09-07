package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const connectedMCPFixture = `#!/usr/bin/env python3
import sys,json,os
args=sys.argv[1:]
if args==['version']: print('mcp fixture');sys.exit(0)
assert args[0] in ('discover','request','tool','read','template-read','prompt'),args
endpoint=args[args.index('--')+1:]
assert endpoint
if endpoint[0].startswith('http'):
 assert '-header-fd' in args,'missing credential descriptor'
 header=os.read(int(args[args.index('-header-fd')+1]),65536).decode()
 assert header=='Authorization: Bearer service-secret\n',header
 assert 'service-secret' not in ' '.join(sys.argv)
 assert 'service-secret' not in json.dumps(dict(os.environ))
 index=args.index('-header-fd');args=args[:index]+args[index+2:]
if os.getenv('FAIL_CONNECTION'):
 print('service-secret connection refused',file=sys.stderr);sys.exit(2)
tools=[{'name':'find_contact','title':'Find a contact','description':'Look up a contact by email.','inputSchema':{'type':'object','required':['email'],'properties':{'email':{'type':'string'}}},'annotations':{'readOnlyHint':True}},{'name':'create_contact','title':'Create a contact','description':'Create a customer record.','inputSchema':{'type':'object','required':['name'],'properties':{'name':{'type':'string'}}}}]
if args[0]=='discover': print(json.dumps({'serverInfo':{'name':'Example CRM','version':'1'},'capabilities':{'tools':{},'resources':{},'prompts':{}}}));sys.exit(0)
if args[0]=='request':
 method=args[1]
 if method=='tools/list': result={'tools':tools}
 elif method=='resources/list': result={'resources':[{'uri':'crm://guide','name':'Service guide','description':'How to keep contact records accurate.'}]}
 elif method=='resources/templates/list': result={'resourceTemplates':[]}
 elif method=='prompts/list': result={'prompts':[{'name':'contact-review','description':'A suggested contact review procedure.'}]}
 else: sys.exit(2)
 print(json.dumps(result));sys.exit(0)
print(json.dumps({'content':[{'type':'text','text':'Contact found.'}],'structuredContent':{'contact':'Ada'}}))
`
const connectedBoxFixture = `#!/usr/bin/env python3
import sys,json,pathlib,subprocess,hashlib,os
args=sys.argv[1:]
if args==['version']: print('mcpbox fixture');sys.exit(0)
kinds={'tools':('tools/list','tools'),'resources':('resources/list','resources'),'templates':('resources/templates/list','resourceTemplates'),'prompts':('prompts/list','prompts')}
if args[0]=='make':
 assert args[1]=='-mcp';mcp=args[2];root=pathlib.Path(args[3]);assert not root.exists();endpoint=args[5:]
 def invoke(argv):
  p=subprocess.run([mcp,*argv,'--',*endpoint],input='{}',text=True,capture_output=True)
  if p.returncode: print(p.stderr,file=sys.stderr);sys.exit(p.returncode)
  return json.loads(p.stdout)
 discovery=invoke(['discover'])
 root.mkdir(parents=True)
 for folder in ['catalog','admit','tools','actions','prompts','bin']: (root/folder).mkdir()
 (root/'discover.json').write_text(json.dumps(discovery));(root/'endpoint.json').write_text(json.dumps({'argv':endpoint}));(root/'runtime.json').write_text(json.dumps({'mcp':mcp}))
 for kind,(method,key) in kinds.items():
  records=invoke(['request',method])[key]
  (root/'catalog'/f'{kind}.jsonl').write_text(''.join(json.dumps(item)+'\n' for item in records))
  (root/'admit'/f'{kind}.tsv').write_text('')
 (root/'admit'/'actions.tsv').write_text('')
 sys.exit(0)
if args[0] in kinds:
 kind=args[0];root=pathlib.Path(args[1])
 for line in (root/'catalog'/f'{kind}.jsonl').read_text().splitlines():
  entry=json.loads(line);name=entry.get('uri') or entry.get('uriTemplate') or entry['name']
  print(name+'\t'+hashlib.sha256((kind+line).encode()).hexdigest()+'\t'+entry.get('description',''))
 sys.exit(0)
if args[0]=='admit':
 import shlex
 root=pathlib.Path(args[1]);kind=args[2];mcp=json.loads((root/'runtime.json').read_text())['mcp'];endpoint=json.loads((root/'endpoint.json').read_text())['argv']
 for name in args[3:]:
  source='tools' if kind=='actions' else kind
  entries=[json.loads(line) for line in (root/'catalog'/f'{source}.jsonl').read_text().splitlines()]
  entry=next(item for item in entries if (item.get('uri') or item.get('uriTemplate') or item['name'])==name)
  if kind=='actions':
   descriptor=json.dumps({'version':1,'name':name,'description':entry.get('description',''),'input_schema':entry['inputSchema']},separators=(',',':'))
   command=shlex.join([mcp,'tool','-expect','fixture-digest',name,'--',*endpoint])
   body='#!/bin/sh\ncase "$1" in\ndescribe) printf "%s\\n" '+shlex.quote(descriptor)+';;\nrun) exec '+command+';;\n*) exit 2;;\nesac\n'
   path=root/'actions'/name
  else:
   verb='read' if kind=='resources' else 'template-read'
   path=root/'bin'/('read' if kind=='resources' else 'read-template')
   body='#!/bin/sh\nexec '+shlex.join([mcp,verb,'-expect','fixture-digest'])+' "$@" '+shlex.join(['--',*endpoint])+'\n'
  path.write_text(body);path.chmod(0o700)
  with (root/'admit'/f'{kind}.tsv').open('a') as f:f.write(name+'\tfixture-digest\n')
 sys.exit(0)
print('unsupported fixture command',file=sys.stderr);sys.exit(2)
`

const connectedActionFixture = `#!/usr/bin/env python3
import sys,os,json,subprocess,pathlib,hashlib
args=sys.argv[1:]
if args==['version']:print('action fixture');sys.exit(0)
def flag(name):return args[args.index(name)+1]
proposal=json.loads(pathlib.Path(flag('-proposal')).read_text());connector=pathlib.Path(os.environ['ACTION_PATH'])/proposal['connector']
canonical=json.dumps(proposal['input'],sort_keys=True,separators=(',',':'))
envelope={'version':1,'job':flag('-job'),'connector':proposal['connector'],'connector_path':str(connector),'connector_sha256':'sha256:'+hashlib.sha256(connector.read_bytes()).hexdigest(),'directory':os.getcwd(),'input':proposal['input']}
raw=json.dumps(envelope,separators=(',',':'))+'\n'
policy=subprocess.run([flag('-policy')],input=raw,text=True,capture_output=True)
assert not policy.stderr,policy.stderr
decision=json.loads(policy.stdout);assert decision['action_sha256']=='sha256:'+hashlib.sha256(raw.encode()).hexdigest()
if policy.returncode:sys.exit(policy.returncode)
assert decision['decision']=='allow'
if 'uncertain' in canonical:print('Outcome unknown; do not retry.',file=sys.stderr);sys.exit(125)
result=subprocess.run([str(connector),'run'],input=canonical,text=True,capture_output=True)
print(result.stdout,end='');print(result.stderr,end='',file=sys.stderr);sys.exit(result.returncode)
`

func configureConnectedFixture(t *testing.T, a *application) {
	t.Helper()
	dir := t.TempDir()
	a.tools.paths["mcp"] = writeScript(t, dir, "mcp", connectedMCPFixture)
	a.tools.paths["mcpbox"] = writeScript(t, dir, "mcpbox", connectedBoxFixture)
	a.tools.paths["mcp-legacy"] = a.tools.paths["mcp"]
	a.tools.paths["action"] = writeScript(t, dir, "action", connectedActionFixture)
}
func awaitConnectedApp(t *testing.T, a *application, id string) ConnectedApp {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		a.appsMu.Lock()
		c, err := a.loadConnectedApp(id)
		active := a.appOperations[id] != nil
		a.appsMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if !active {
			return c
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("connection operation did not finish")
	return ConnectedApp{}
}
func createConnectedFixture(t *testing.T, a *application) ConnectedApp {
	t.Helper()
	rec, data := call(t, a.routes(), "POST", "/api/connections", appConnectInput{Name: "Sales CRM", Transport: "http", URL: "https://crm.example/mcp", Auth: "token", Token: "service-secret"})
	if rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	c := awaitConnectedApp(t, a, data["connection"].(map[string]any)["id"].(string))
	if c.State != "ready" {
		t.Fatal(c.Error)
	}
	return c
}
func TestConnectedDiscoveryRetainsCatalogueWithoutCredentialsOrGrants(t *testing.T) {
	a, _, _ := newTestApp(t)
	configureConnectedFixture(t, a)
	c := createConnectedFixture(t, a)
	rec, data := call(t, a.routes(), "GET", "/api/connections/"+c.ID, nil)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "service-secret") {
		t.Fatal(rec.Body.String())
	}
	catalog := data["connection"].(map[string]any)["catalogue"].(map[string]any)
	if len(catalog["capabilities"].([]any)) != 4 {
		t.Fatal("missing tool, resource or prompt descriptions")
	}
	dir, _ := a.appDir(c.ID)
	for _, kind := range []string{"tools", "actions"} {
		entries, err := os.ReadDir(filepath.Join(dir, "catalogues", c.CatalogueID, "box", kind))
		if err != nil || len(entries) > 0 {
			t.Fatal("discovery granted a capability", err)
		}
	}
	info, err := os.Stat(filepath.Join(dir, "credentials.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("credentials are not private")
	}
	files, err := readSkillBundle(filepath.Join(dir, "catalogues", c.CatalogueID))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files.Files {
		if file.Dir {
			continue
		}
		raw, _ := os.ReadFile(filepath.Join(dir, "catalogues", c.CatalogueID, file.Path))
		if strings.Contains(string(raw), "service-secret") {
			t.Fatalf("credential copied into %s", file.Path)
		}
	}
	rec, _ = call(t, a.routes(), "POST", "/api/connections/"+c.ID+"/disconnect", nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	rec, _ = call(t, a.routes(), "POST", "/api/connections/"+c.ID+"/reconnect", nil)
	if rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	next := awaitConnectedApp(t, a, c.ID)
	if next.CatalogueID == c.CatalogueID || next.State != "ready" {
		t.Fatal("reconnect did not produce a fresh reviewable catalogue")
	}
}
func TestConnectedInputAndOrphanRecovery(t *testing.T) {
	a, _, _ := newTestApp(t)
	configureConnectedFixture(t, a)
	for _, in := range []appConnectInput{{Name: "Bad", URL: "https://secret@service.example/mcp"}, {Name: "Bad", URL: "https://service.example/mcp?api_key=secret"}, {Name: "Bad", URL: "https://service.example/mcp", Auth: "headers", Headers: map[string]string{"Mcp-Method": "tools/call"}}, {Name: "Bad", URL: "https://service.example/mcp", Auth: "token", Token: "secret\r\nInjected: yes"}} {
		rec, _ := call(t, a.routes(), "POST", "/api/connections", in)
		if rec.Code != 400 {
			t.Fatal("unsafe connection accepted:", rec.Body.String())
		}
	}
	c := createConnectedFixture(t, a)
	c.State = "checking"
	if err := a.saveConnectedApp(c); err != nil {
		t.Fatal(err)
	}
	rec, _ := call(t, a.routes(), "GET", "/api/connections/"+c.ID, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "interrupted") {
		t.Fatal("orphaned operation stayed busy")
	}
	if len(a.appOperations) != 0 {
		t.Fatal("orphaned operation retried itself")
	}
}
func TestRealSuiteConnectedDiscovery(t *testing.T) {
	bin := os.Getenv("HIRE_INTEGRATION_BIN_DIR")
	if bin == "" {
		t.Skip("set HIRE_INTEGRATION_BIN_DIR for real MCPbox/MCP/MCPserve")
	}
	bin, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := newTestApp(t)
	for _, name := range []string{"agent", "ply", "brief", "trail", "mcp", "mcpbox", "mcpserve", "action", "ask", "cage", "context", "cite"} {
		a.tools.paths[name] = filepath.Join(bin, name)
	}
	dir := t.TempDir()
	manifest := filepath.Join(dir, "service.json")
	raw := `{"name":"Hiring CRM","version":"1","tools":[{"name":"find_contact","description":"Find a contact.","inputSchema":{"type":"object","properties":{"email":{"type":"string"}}}}],"resources":[{"uri":"crm://guide","name":"Service guide"}],"resourceTemplates":[{"uriTemplate":"crm://contacts/{id}","name":"Contact record"}]}`
	if err := os.WriteFile(manifest, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	dispatch := writeScript(t, dir, "dispatch", "#!/usr/bin/env python3\nimport json,sys\np=json.load(sys.stdin)\nresult={'contents':[{'uri':p['uri'],'text':'Use exact email matching.'}]} if sys.argv[-1]=='resources/read' else {'content':[{'type':'text','text':'Contact found.'}]}\nprint(json.dumps(result))\n")
	rec, data := call(t, a.routes(), "POST", "/api/connections", appConnectInput{Name: "Hiring CRM", Transport: "stdio", Command: filepath.Join(bin, "mcpserve"), Args: []string{manifest, "--", dispatch}})
	if rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	c := awaitConnectedApp(t, a, data["connection"].(map[string]any)["id"].(string))
	if c.State != "ready" {
		t.Fatal(c.Error)
	}
	catalog, err := a.appCatalogue(c)
	if err != nil || len(catalog.Capabilities) != 3 {
		t.Fatalf("catalogue: %+v %v", catalog, err)
	}
	// Public MCPbox, not Hire, assigns the descriptor digest.
	root, _ := a.appDir(c.ID)
	out, stderr, code, err := a.connectedCommand(context.Background(), "mcpbox", []string{"tools", filepath.Join(root, "catalogues", c.CatalogueID, "box")}, nil, 5*time.Second)
	if err != nil || code != 0 || !strings.Contains(string(out), catalog.Capabilities[0].Digest) {
		t.Fatalf("public digest: %s %s %d %v", out, stderr, code, err)
	}
	public, _ := json.Marshal(catalog)
	if strings.Contains(string(public), "credentials.json") {
		t.Fatal("credential path leaked into catalogue")
	}
	worker, _, err := a.createWorker(context.Background(), createWorkerRequest{Name: "CRM assistant", Purpose: "Find customer contact information"})
	if err != nil {
		t.Fatal(err)
	}
	rec, _ = call(t, a.routes(), "PUT", "/api/workers/"+worker.Slug+"/connections/"+c.ID, map[string]any{"catalogueSha256": catalog.SHA256, "permissions": []AppPermission{{Kind: "tools", Name: "find_contact", Digest: catalog.Capabilities[0].Digest, Mode: "automatic"}}})
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	grant, err := readAppGrant(a.homeDir(worker.Slug), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	permissions := []AppPermission{}
	for _, cap := range catalog.Capabilities {
		permissions = append(permissions, AppPermission{Kind: cap.Kind, Name: cap.Name, Digest: cap.Digest, Mode: "automatic"})
	}
	rec, _ = call(t, a.routes(), "PUT", "/api/workers/"+worker.Slug+"/connections/"+c.ID, map[string]any{"catalogueSha256": catalog.SHA256, "version": grant.Version, "permissions": permissions})
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	grant, _ = readAppGrant(a.homeDir(worker.Slug), c.ID)
	for _, read := range []appCallRequest{{ConnectionID: c.ID, Operation: "read", Name: "crm://guide"}, {ConnectionID: c.ID, Operation: "read-template", Name: "crm://contacts/{id}", URI: "crm://contacts/ada", Input: json.RawMessage(`{}`)}} {
		response := a.executeAppCall(context.Background(), worker, "resource-task", read)
		if response.Exit != 0 || response.Call == nil || response.Call.Source == nil {
			t.Fatalf("resource read: %+v %+v", response, response.Call)
		}
	}
	session := createConnectedTestSession(t, a, worker)
	for _, id := range []string{"request-1", "request-2"} {
		if err := writeFileAtomic(filepath.Join(a.homeDir(worker.Slug), ".agent", "checkpoints", id+".current"), []byte(session+"\n"), false); err != nil {
			t.Fatal(err)
		}
	}
	response := a.executeAppCall(context.Background(), worker, "request-1", appCallRequest{ConnectionID: c.ID, Operation: "run", Name: "find_contact", Input: json.RawMessage(`{"email":"ada@example.test"}`)})
	if response.Exit != 0 || response.Call == nil || response.Call.State != "succeeded" {
		t.Fatalf("real call: %+v call %+v", response, response.Call)
	}
	out, errout, code, err := a.connectedCommand(context.Background(), "ask", []string{"replay", "-check", session}, nil, 5*time.Second)
	if err != nil || code != 0 {
		t.Fatalf("Action receipt: %s %s %d %v", out, errout, code, err)
	}
	repeated := a.executeAppCall(context.Background(), worker, "request-1", appCallRequest{ConnectionID: c.ID, Operation: "run", Name: "find_contact", Input: json.RawMessage(`{"email":"ada@example.test"}`)})
	if repeated.Call == nil || repeated.Call.ID != response.Call.ID {
		t.Fatal("repeated effect was not deduplicated")
	}
	closeBroker, err := a.startAppBroker(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer closeBroker()
	tool := filepath.Join(a.homeDir(worker.Slug), "tools", grant.Command)
	denied := exec.Command(tool, "run", "find_contact")
	denied.Stdin = strings.NewReader(`{"email":"other@example.test"}`)
	if raw, err := denied.CombinedOutput(); err == nil || !strings.Contains(string(raw), "active Hire task") {
		t.Fatalf("unbound process reached app: %s %v", raw, err)
	}
	closeExecution, err := recordAppExecution(a.store.workerDir(worker.Slug), a.homeDir(worker.Slug), "request-2")
	if err != nil {
		t.Fatal(err)
	}
	defer closeExecution()
	caged := exec.Command(a.tools.path("cage"), "-w", filepath.Join(a.homeDir(worker.Slug), "work"), "--", tool, "run", "find_contact")
	caged.Stdin = strings.NewReader(`{"email":"confined@example.test"}`)
	if raw, err := caged.CombinedOutput(); err != nil || !strings.Contains(string(raw), `"state":"succeeded"`) {
		t.Fatalf("confined app call: %s %v", raw, err)
	}
	rec, _ = call(t, a.routes(), "DELETE", "/api/workers/"+worker.Slug+"/connections/"+c.ID, nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	response = a.executeAppCall(context.Background(), worker, "request-3", appCallRequest{ConnectionID: c.ID, Operation: "run", Name: "find_contact", Input: json.RawMessage(`{}`)})
	if response.Exit != 2 || response.Call != nil {
		t.Fatal("revoked app ran")
	}
	closeExecution()
	grant = connectedGrantFixture(t, a, worker, c, "automatic")
	for _, name := range []string{"agent", "ply", "brief", "cage", "trail"} {
		a.tools.paths[name] = filepath.Join(bin, name)
	}
	for _, entry := range a.tools.agentEnvironment() {
		name, value, _ := strings.Cut(entry, "=")
		t.Setenv(name, value)
	}
	t.Setenv("HIRE_AGENT", a.tools.path("agent"))
	request := Request{ID: "actual-agent-task", WorkerSlug: worker.Slug, Kind: "request", Title: "Find the customer", Text: "Find the customer in the CRM and report the source citation.", Source: "manager", Model: "openai/fixture", Runs: true, CreatedAt: a.now(), NotBefore: a.now()}
	if err := a.store.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	var modelCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelCalls.Add(1) > 2 {
			http.Error(w, "The app test failed to produce a checked result", 400)
			return
		}
		body := `set -eu
printf '%s' '{"email":"agent@example.test"}' | ` + shellQuote(filepath.Join(a.homeDir(worker.Slug), "tools", grant.Command)) + ` run find_contact > service-result.json
python3 - <<'PY'
import json,pathlib
call=json.loads(pathlib.Path('service-result.json').read_text())['call']
assert call['state']=='succeeded'
source=call['source']
pathlib.Path('requests/actual-agent-task/RESULT.md').write_text('Customer found. ['+source['ref']+']('+source['url']+')\n')
PY`
		reply := "```sh\n" + body + "\n```"
		if modelCalls.Load() > 1 {
			reply = "The customer result is ready for the task's completion check."
		}
		item := map[string]any{"type": "message", "id": "msg_worker", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": reply, "annotations": []any{}}}}
		complete := map[string]any{"type": "response.completed", "sequence_number": 2, "response": map[string]any{"id": "resp_worker", "object": "response", "created_at": 1, "status": "completed", "model": "fixture", "output": []any{item}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 10, "total_tokens": 20, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "input_tokens_details": map[string]any{"cached_tokens": 0}}}}
		raw, _ := json.Marshal(complete)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", raw)
	}))
	defer server.Close()
	t.Setenv("OPENAI_BASE_URL", server.URL)
	t.Setenv("OPENAI_API_KEY", "offline-fixture")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.executable, "exec", a.homeDir(worker.Slug), filepath.Join(a.store.workerDir(worker.Slug), "requests", request.ID+".json"))
	if raw, err := cmd.CombinedOutput(); err != nil {
		result, _ := os.ReadFile(filepath.Join(a.homeDir(worker.Slug), "work", "service-result.json"))
		delivery, deliveryErr := os.ReadFile(filepath.Join(a.homeDir(worker.Slug), "work", "requests", request.ID, "RESULT.md"))
		check := exec.Command(filepath.Join(a.homeDir(worker.Slug), "bin", "check"))
		check.Dir = filepath.Join(a.homeDir(worker.Slug), "work")
		checkOut, checkErr := check.CombinedOutput()
		t.Fatalf("actual Agent app task: %s %v\nService result: %s\nDelivery: %s %v\nCheck: %s %v", raw, err, result, delivery, deliveryErr, checkOut, checkErr)
	}
	rec, data = call(t, a.routes(), "GET", "/api/workers/"+worker.Slug+"/requests/"+request.ID+"/citations", nil)
	if rec.Code != 200 || data["state"] != "valid" {
		t.Fatal("Agent result citations:", rec.Body.String())
	}
	// A service update must not inherit admission simply because its tool
	// retained the same name. Native MCP checks the descriptor before use.
	changed := strings.Replace(raw, "Find a contact.", "Find a contact with the revised procedure.", 1)
	if err := os.WriteFile(manifest, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	pointer, _ := os.ReadFile(filepath.Join(a.homeDir(worker.Slug), ".agent", "checkpoints", request.ID+".current"))
	if err := writeFileAtomic(filepath.Join(a.homeDir(worker.Slug), ".agent", "checkpoints", "changed-task.current"), pointer, false); err != nil {
		t.Fatal(err)
	}
	response = a.executeAppCall(context.Background(), worker, "changed-task", appCallRequest{ConnectionID: c.ID, Operation: "run", Name: "find_contact", Input: json.RawMessage(`{"email":"changed@example.test"}`)})
	if response.Exit != 2 || response.Call == nil || response.Call.State != "stopped" {
		t.Fatalf("changed descriptor ran: %+v %+v", response, response.Call)
	}
	rec, _ = call(t, a.routes(), "POST", "/api/connections/"+c.ID+"/check", nil)
	if rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	awaitConnectedApp(t, a, c.ID)
	rec, _ = call(t, a.routes(), "GET", "/api/workers/"+worker.Slug+"/connections", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"accessChanged":true`) {
		t.Fatal("changed capability was not exposed for review", rec.Body.String())
	}
}

func createConnectedTestSession(t *testing.T, a *application, worker Worker) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item := map[string]any{"type": "message", "id": "msg_app", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Inspect the employee's app access.", "annotations": []any{}}}}
		complete := map[string]any{"type": "response.completed", "sequence_number": 2, "response": map[string]any{"id": "resp_app", "object": "response", "created_at": 1, "status": "completed", "model": "fixture", "output": []any{item}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 10, "total_tokens": 20, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "input_tokens_details": map[string]any{"cached_tokens": 0}}}}
		raw, _ := json.Marshal(complete)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", raw)
	}))
	defer server.Close()
	t.Setenv("OPENAI_BASE_URL", server.URL)
	t.Setenv("OPENAI_API_KEY", "offline-fixture")
	session := filepath.Join(a.homeDir(worker.Slug), ".agent", "runs", "apps.jsonl")
	if err := os.MkdirAll(filepath.Dir(session), 0700); err != nil {
		t.Fatal(err)
	}
	_, stderr, code, err := a.connectedCommand(context.Background(), "ask", []string{"-q", "-m", "openai/fixture", "-f", session, "Inspect app access"}, nil, 10*time.Second)
	if err != nil || code != 0 {
		t.Fatalf("create native test session: %s %d %v", stderr, code, err)
	}
	return session
}
