package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestModelArgsAndHeaderInsertion(t *testing.T) {
	t.Setenv("ASK_MODEL", "")
	if got := modelFromArgs([]string{"-q", "-m", "openai-codex/x", "hello"}); got != "openai-codex/x" {
		t.Fatalf("modelFromArgs = %q", got)
	}
	if got := modelFromArgs([]string{"-m=anthropic/y"}); got != "anthropic/y" {
		t.Fatalf("modelFromArgs = %q", got)
	}
	t.Setenv("ASK_MODEL", "gemini/z")
	if got := modelFromArgs([]string{"hello"}); got != "gemini/z" {
		t.Fatalf("modelFromArgs env = %q", got)
	}
	args, needed := headerArgs([]string{"-q", "-m", "openai-codex/x", "hi"})
	if !needed || strings.Join(args, " ") != "-header-fd 3 -q -m openai-codex/x hi" {
		t.Fatalf("headerArgs = %v %v", args, needed)
	}
	args, needed = headerArgs([]string{"compact", "-m", "openai-codex/x", "s.jsonl"})
	if !needed || strings.Join(args, " ") != "compact -header-fd 3 -m openai-codex/x s.jsonl" {
		t.Fatalf("headerArgs compact = %v %v", args, needed)
	}
	if _, needed = headerArgs([]string{"note", "-s", "src", "-f", "s.jsonl"}); needed {
		t.Fatal("note makes no model call and must pass through untouched")
	}
}

func TestJWTExpiryAndAccount(t *testing.T) {
	exp := time.Date(2026, 9, 7, 12, 43, 54, 0, time.UTC)
	token := makeJWT(t, map[string]any{"exp": exp.Unix()})
	if got := jwtExpiry(token); !got.Equal(exp) {
		t.Fatalf("jwtExpiry = %s", got)
	}
	if !jwtExpiry("not-a-jwt").IsZero() {
		t.Fatal("garbage must have no expiry")
	}
	id := makeJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1"}})
	if got := jwtAccountID(id); got != "acct-1" {
		t.Fatalf("jwtAccountID = %q", got)
	}
}

func TestValidateModel(t *testing.T) {
	if err := validateModel("openai-codex/gpt-5.6-sol"); err != nil {
		t.Fatal(err)
	}
	err := validateModel("codex/gpt-5.6-sol")
	if err == nil || !strings.Contains(err.Error(), "openai-codex/gpt-5.6-sol") {
		t.Fatalf("shorthand should be corrected, got %v", err)
	}
	if err := validateModel("gpt-5.6-sol"); err == nil {
		t.Fatal("a bare model name must be refused")
	}
	for _, model := range []string{"mystery/x", "deepseek/reasoner", "cerebras/model", "openrouter/vendor/model"} {
		if err := validateModel(model); err != nil {
			t.Fatalf("Ask owns support for %q: %v", model, err)
		}
	}
	for _, model := range []string{"/model", "provider/", "provider/model\nflag", "-flag/model"} {
		if err := validateModel(model); err == nil {
			t.Fatalf("invalid model shape accepted: %q", model)
		}
	}
}

// The wrapper must refresh an expired Codex CLI token into Hire's own copy and
// deliver the header to ask on descriptor 3, never in argv or the environment.
func TestAskShimRefreshesAndDeliversHeader(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	expired := makeJWT(t, map[string]any{"exp": time.Now().Add(-time.Hour).Unix()})
	cli := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{"access_token": expired, "refresh_token": "refresh-1", "account_id": "acct-cli"}, "last_refresh": time.Now().Add(-48 * time.Hour)}
	data, _ := json.Marshal(cli)
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := makeJWT(t, map[string]any{"exp": time.Now().Add(2 * time.Hour).Unix()})
	var refreshes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes++
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh-1" || r.Form.Get("client_id") != codexClientID {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": fresh, "refresh_token": "refresh-2"})
	}))
	defer server.Close()

	fakeAsk := writeScript(t, root, "ask", "#!/bin/sh\nprintf 'fd3=%s\\n' \"$(cat <&3)\"\necho \"args=$*\"\necho \"account=$OPENAI_CODEX_ACCOUNT_ID\"\n")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(root, "codex-token.json")
	run := func(args ...string) string {
		cmd := exec.Command(self, append([]string{"-test.run", "TestHelperExec", "--", "ask"}, args...)...)
		cmd.Env = append(os.Environ(), "CODEX_HOME="+codexHome, "HIRE_REAL_ASK="+fakeAsk, "HIRE_CODEX_CACHE="+cache, "HIRE_CODEX_TOKEN_URL="+server.URL, "HIRE_OAUTH_PROFILE=", "OPENAI_CODEX_ACCOUNT_ID=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("shim: %v\n%s", err, out)
		}
		return string(out)
	}
	out := run("-q", "-m", "openai-codex/gpt-5.6-sol", "hello")
	if !strings.Contains(out, "fd3=Authorization: Bearer "+fresh) {
		t.Fatalf("header not delivered on fd 3:\n%s", out)
	}
	if !strings.Contains(out, "args=-header-fd 3 -q -m openai-codex/gpt-5.6-sol hello") || strings.Contains(out, fresh+" ") {
		t.Fatalf("argv wrong or token leaked into argv:\n%s", out)
	}
	if !strings.Contains(out, "account=acct-cli") {
		t.Fatalf("account id not passed:\n%s", out)
	}
	info, err := os.Stat(cache)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache should be private: %v %v", info, err)
	}
	var cached codexTokens
	if err := readJSON(cache, &cached); err != nil || cached.RefreshToken != "refresh-2" || cached.Source != "hire-refresh" {
		t.Fatalf("cache = %+v %v", cached, err)
	}
	if refreshes != 1 {
		t.Fatalf("expected one refresh, got %d", refreshes)
	}
	// Second call uses the cached token: no refresh, same header.
	out = run("-m", "openai-codex/gpt-5.6-sol", "again")
	if refreshes != 1 || !strings.Contains(out, "fd3=Authorization: Bearer "+fresh) {
		t.Fatalf("second call should reuse the cache (refreshes=%d):\n%s", refreshes, out)
	}
	// Other providers pass straight through with no descriptor and no header.
	out = run("-m", "anthropic/x", "plain")
	if strings.Contains(out, "Bearer") || !strings.Contains(out, "args=-m anthropic/x plain") {
		t.Fatalf("pass-through changed the call:\n%s", out)
	}
	// A subcommand that makes no model call is untouched even for codex.
	out = run("note", "-s", "x", "-f", filepath.Join(root, "s.jsonl"), "-m", "openai-codex/x")
	if strings.Contains(out, "header-fd") {
		t.Fatalf("note should pass through:\n%s", out)
	}
}

func TestAskShimScriptQuoting(t *testing.T) {
	script := askShimScript("/opt/it's/hire", "/usr/local/bin/ask", "/data/hire/codex-token.json", "")
	if !strings.Contains(script, `'/opt/it'\''s/hire'`) || !strings.Contains(script, `exec '/opt/it'\''s/hire' ask "$@"`) {
		t.Fatalf("script = %q", script)
	}
}
