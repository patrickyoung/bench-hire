package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const connectedLegacyServer = `#!/usr/bin/env python3
import json,sys
for line in sys.stdin:
 request=json.loads(line)
 if 'id' not in request:continue
 method=request['method']
 if method=='initialize':result={'protocolVersion':'2025-03-26','capabilities':{'tools':{}},'serverInfo':{'name':'Existing CRM','version':'1'}}
 elif method=='tools/list':result={'tools':[{'name':'find_contact','description':'Find the customer by exact email.','inputSchema':{'type':'object','properties':{'email':{'type':'string'}}}}]}
 elif method=='tools/call':result={'content':[{'type':'text','text':'Customer Ada found in the existing service.'}]}
 elif method=='resources/list':result={'resources':[]}
 elif method=='resources/templates/list':result={'resourceTemplates':[]}
 elif method=='prompts/list':result={'prompts':[]}
 elif method=='ping':result={}
 else:print(json.dumps({'jsonrpc':'2.0','id':request['id'],'error':{'code':-32601,'message':'Unknown method'}}),flush=True);continue
 print(json.dumps({'jsonrpc':'2.0','id':request['id'],'result':result}),flush=True)
`

func TestRealSuiteConnectedLegacy(t *testing.T) {
	bin := os.Getenv("HIRE_INTEGRATION_BIN_DIR")
	if bin == "" {
		t.Skip("set HIRE_INTEGRATION_BIN_DIR for real legacy MCP composition")
	}
	bin, _ = filepath.Abs(bin)
	a, _, _ := newTestApp(t)
	for _, name := range []string{"mcp", "mcp-legacy", "mcpbox", "action", "ask", "context", "cite"} {
		a.tools.paths[name] = filepath.Join(bin, name)
	}
	server := writeScript(t, t.TempDir(), "legacy-service", connectedLegacyServer)
	rec, data := call(t, a.routes(), "POST", "/api/connections", appConnectInput{Name: "Existing CRM", Transport: "stdio", Command: server, Protocol: "legacy"})
	if rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	c := awaitConnectedApp(t, a, data["connection"].(map[string]any)["id"].(string))
	if c.State != "ready" {
		t.Fatal(c.Error)
	}
	worker, _, err := a.createWorker(context.Background(), createWorkerRequest{Name: "Existing service assistant", Purpose: "Find customers"})
	if err != nil {
		t.Fatal(err)
	}
	connectedGrantFixture(t, a, worker, c, "automatic")
	session := createConnectedTestSession(t, a, worker)
	if err := writeFileAtomic(filepath.Join(a.homeDir(worker.Slug), ".agent", "checkpoints", "legacy-task.current"), []byte(session+"\n"), false); err != nil {
		t.Fatal(err)
	}
	response := a.executeAppCall(context.Background(), worker, "legacy-task", appCallRequest{ConnectionID: c.ID, Operation: "run", Name: "find_contact", Input: json.RawMessage(`{"email":"ada@example.test"}`)})
	if response.Exit != 0 || response.Call == nil || response.Call.Source == nil {
		t.Fatalf("legacy operation: %+v %+v", response, response.Call)
	}
}
