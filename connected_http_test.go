package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRealSuiteConnectedHTTPToken(t *testing.T) {
	bin := os.Getenv("HIRE_INTEGRATION_BIN_DIR")
	if bin == "" {
		t.Skip("set HIRE_INTEGRATION_BIN_DIR for real HTTP MCP composition")
	}
	bin, _ = filepath.Abs(bin)
	a, _, _ := newTestApp(t)
	for _, name := range []string{"mcp", "mcpbox", "action", "ask", "context", "cite"} {
		a.tools.paths[name] = filepath.Join(bin, name)
	}
	dir := t.TempDir()
	manifest := filepath.Join(dir, "service.json")
	if err := os.WriteFile(manifest, []byte(`{"name":"Online CRM","version":"1","tools":[{"name":"find_contact","description":"Find an exact email match","inputSchema":{"type":"object","properties":{"email":{"type":"string"}}}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	dispatch := writeScript(t, dir, "dispatch", "#!/usr/bin/env python3\nimport json,sys\njson.load(sys.stdin)\nprint(json.dumps({'content':[{'type':'text','text':'Customer found online.'}]}))\n")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(bin, "mcpserve"), "-http", address, manifest, "--", dispatch)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("MCPserve did not listen")
		}
		time.Sleep(10 * time.Millisecond)
	}
	upstream, _ := url.Parse("http://" + address)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	protected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-http-token" {
			http.Error(w, "Service sign-in required", 401)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer protected.Close()
	rec, data := call(t, a.routes(), "POST", "/api/connections", appConnectInput{Name: "Online CRM", URL: protected.URL + "/mcp", Transport: "http", Auth: "token", Token: "fixture-http-token"})
	if rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	c := awaitConnectedApp(t, a, data["connection"].(map[string]any)["id"].(string))
	if c.State != "ready" {
		t.Fatal(c.Error)
	}
	worker, _, err := a.createWorker(context.Background(), createWorkerRequest{Name: "Online assistant", Purpose: "Find customers"})
	if err != nil {
		t.Fatal(err)
	}
	connectedGrantFixture(t, a, worker, c, "automatic")
	session := createConnectedTestSession(t, a, worker)
	if err := writeFileAtomic(filepath.Join(a.homeDir(worker.Slug), ".agent", "checkpoints", "online-task.current"), []byte(session+"\n"), false); err != nil {
		t.Fatal(err)
	}
	response := a.executeAppCall(context.Background(), worker, "online-task", appCallRequest{ConnectionID: c.ID, Operation: "run", Name: "find_contact", Input: json.RawMessage(`{"email":"ada@example.test"}`)})
	if response.Exit != 0 || response.Call == nil || response.Call.Source == nil {
		t.Fatalf("HTTP operation: %+v %+v", response, response.Call)
	}
}
