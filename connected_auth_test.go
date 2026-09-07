package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const connectedOAuthFixture = `#!/usr/bin/env python3
import sys,os,pathlib,time,subprocess,tempfile
args=sys.argv[1:];root=pathlib.Path(os.environ['OAUTH_HOME'])
if args==['version']:print('oauth fixture');sys.exit(0)
if args[0]=='login':
 assert '-no-browser' in args and '-replace' in args
 assert '-client-secret-stdin' in args and sys.stdin.read()=='fixture-client-secret'
 assert 'fixture-client-secret' not in ' '.join(sys.argv)
 assert 'fixture-client-secret' not in str(dict(os.environ))
 root.mkdir(parents=True,exist_ok=True)
 print('oauth: open this URL to authorize '+args[1]+':',file=sys.stderr,flush=True)
 print('https://login.example/authorize?state=temporary-state',file=sys.stderr,flush=True)
 while not (root/'finish-fixture-login').exists():time.sleep(.02)
 (root/'profile-ready').write_text('token retained by OAuth fixture')
 sys.exit(0)
if args[0]=='with':
 assert (root/'profile-ready').exists(),'not signed in'
 command=args[args.index('--')+1:]
 fd,path=tempfile.mkstemp(dir=root);os.unlink(path)
 os.write(fd,b'Authorization: Bearer service-secret\n');os.lseek(fd,0,0)
 if fd!=3:os.dup2(fd,3)
 os.set_inheritable(3,True)
 result=subprocess.run(command,stdin=sys.stdin,pass_fds=(3,));sys.exit(result.returncode)
sys.exit(2)
`

func TestConnectedOAuthProgressSecretBoundaryAndCancellation(t *testing.T) {
	a, _, _ := newTestApp(t)
	configureConnectedFixture(t, a)
	a.tools.paths["oauth"] = writeScript(t, t.TempDir(), "oauth", connectedOAuthFixture)
	in := appConnectInput{Name: "Signed-in CRM", Transport: "http", URL: "https://crm.example/mcp", Auth: "oauth", ClientID: "fixture-client", ClientSecret: "fixture-client-secret"}
	create := func() ConnectedApp {
		rec, data := call(t, a.routes(), "POST", "/api/connections", in)
		if rec.Code != 202 {
			t.Fatal(rec.Body.String())
		}
		id := data["connection"].(map[string]any)["id"].(string)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			rec, _ := call(t, a.routes(), "GET", "/api/connections/"+id, nil)
			if strings.Contains(rec.Body.String(), "fixture-client-secret") {
				t.Fatal("client secret exposed")
			}
			if strings.Contains(rec.Body.String(), "temporary-state") {
				c, _ := a.loadConnectedApp(id)
				return c
			}
			time.Sleep(15 * time.Millisecond)
		}
		t.Fatal("sign-in URL did not appear")
		return ConnectedApp{}
	}
	c := create()
	dir, _ := a.appDir(c.ID)
	raw, _ := os.ReadFile(filepath.Join(dir, "connection.json"))
	if strings.Contains(string(raw), "temporary-state") {
		t.Fatal("authorization URL persisted")
	}
	if err := os.WriteFile(filepath.Join(dir, "oauth", "finish-fixture-login"), []byte("finish"), 0600); err != nil {
		t.Fatal(err)
	}
	c = awaitConnectedApp(t, a, c.ID)
	if c.State != "ready" {
		t.Fatal(c.Error)
	}
	rec, _ := call(t, a.routes(), "GET", "/api/connections/"+c.ID, nil)
	if strings.Contains(rec.Body.String(), "temporary-state") || strings.Contains(rec.Body.String(), "service-secret") {
		t.Fatal("completed sign-in leaked temporary material")
	}
	cancelled := create()
	rec, _ = call(t, a.routes(), "POST", "/api/connections/"+cancelled.ID+"/disconnect", nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	time.Sleep(80 * time.Millisecond)
	rec, _ = call(t, a.routes(), "GET", "/api/connections/"+cancelled.ID, nil)
	if strings.Contains(rec.Body.String(), "temporary-state") || !strings.Contains(rec.Body.String(), `"state":"disconnected"`) {
		t.Fatal("cancelled sign-in revived", rec.Body.String())
	}
}
