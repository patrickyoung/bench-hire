package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeAgent mimics the public agent commands Hire composes. A run writes the
// RESULT.md, honours the request's optional check by running the home's real
// bin/check, and exits 0 or 2 exactly as agent would.
const fakeAgent = `#!/bin/sh
cmd=$1; shift
case "$cmd" in
  version) echo "agent fake"; exit 0;;
  new) mkdir -p "$1/bin" "$1/state" "$1/work"; printf '# Outcome\n' > "$1/GOAL.md"; printf '# Ops\n' > "$1/AGENTS.md"; printf '#!/bin/sh\nexit 1\n' > "$1/bin/check"; printf '#!/bin/sh\nexit 0\n' > "$1/bin/wake"; chmod +x "$1/bin/check" "$1/bin/wake"; exit 0;;
  check) [ -s "$1/GOAL.md" ] && [ -x "$1/bin/check" ] && ! grep -q BREAK "$1/GOAL.md" || { echo "agent: GOAL.md rejected" >&2; exit 1; }; exit 0;;
  show) echo "definition-sha256: def123"; echo "compiled-sha256: comp456"; echo "check-sha256: chk789"; echo "default-authority: full host tool catalogue; Cage writes work+state; network denied"; exit 0;;
  history) echo '{"session":"s1","goal":"x"}'; exit 0;;
  run)
    home=""
    while [ $# -gt 0 ]; do
      case "$1" in -net|-m|-checkpoint) [ "$1" = "-net" ] || shift;; --) shift; break;; *) home=$1;; esac
      shift
    done
    id=$(sed -n 's/^id: //p' "$home/REQUEST.md" | head -1)
    echo "fake run for $id: $*" >&2
    if grep -q 'NOWRITE' "$home/REQUEST.md"; then echo "did not write" >&2; else
      mkdir -p "$home/work/requests/$id"; printf 'Summary line for %s\n\nbody\n' "$id" > "$home/work/requests/$id/RESULT.md"
    fi
    (cd "$home/work" && "$home/bin/check" </dev/null) && exit 0
    exit 2;;
esac
echo "fake agent: unknown $cmd" >&2; exit 2
`

const fakeAsk = `#!/bin/sh
if [ "$1" = "version" ]; then echo "ask fake"; exit 0; fi
if [ -n "$FAKE_ASK_LOG" ]; then printf '%s\n' "$*" >> "$FAKE_ASK_LOG"; fi
if [ -n "$FAKE_ASK_INPUT_LOG" ]; then { printf '=== %s\n' "$*"; cat; printf '\n'; } >> "$FAKE_ASK_INPUT_LOG"; fi
[ -z "$FAKE_ASK_SLEEP" ] || sleep "$FAKE_ASK_SLEEP"
case "$*" in
  *builder-router-schema.json*) cat "${FAKE_BUILDER_ROUTER_REPLY:-$FAKE_ASK_REPLY}";;
  *builder-expert-schema.json*)
    n=$(printf '%s' "$*" | sed -n 's/.*-expert-\([0-9][0-9]\)\.jsonl.*/\1/p')
    eval "nap=\${FAKE_BUILDER_EXPERT_SLEEP_$n:-}"
    [ -z "$nap" ] || sleep "$nap"
    eval "reply=\${FAKE_BUILDER_EXPERT_REPLY_$n:-}"
    cat "${reply:-${FAKE_BUILDER_EXPERT_REPLY:-$FAKE_ASK_REPLY}}";;
  *agent-builder-schema.json*) cat "${FAKE_BUILDER_SYNTHESIS_REPLY:-$FAKE_ASK_REPLY}";;
  *) cat "$FAKE_ASK_REPLY";;
esac
`

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestApp(t *testing.T) (*application, *memoryJobs, func(time.Duration)) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	agentPath := writeScript(t, bin, "agent", fakeAgent)
	askPath := writeScript(t, bin, "ask", fakeAsk)
	t.Setenv("HIRE_AGENT", agentPath)
	t.Setenv("HIRE_ASK", askPath)
	for _, name := range []string{"HIRE_TEND", "HIRE_PLY", "HIRE_BRIEF", "HIRE_CAGE"} {
		t.Setenv(name, agentPath)
	}
	store, err := newStore(filepath.Join(root, "hire"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	clock := &now
	jobs := newMemoryJobs(filepath.Join(root, "tend"), func() time.Time { return *clock })
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Tests run inside the test binary; route "exec" through a tiny wrapper so
	// memoryJobs can run the real runExec path.
	wrapper := writeScript(t, bin, "hire", "#!/bin/sh\nexec \""+self+"\" -test.run TestHelperExec -- \"$@\"\n")
	app := &application{
		store:       store,
		jobs:        jobs,
		tools:       newToolset(""),
		dataRoot:    root,
		workersRoot: filepath.Join(root, "workers"),
		askDir:      filepath.Join(root, "ask"),
		executable:  wrapper,
		location:    time.UTC,
		now:         func() time.Time { return *clock },
		model:       "openai/fake-model",
		token:       "test-token",
		host:        "127.0.0.1:8790",
	}
	app.runner = newRunner(app, 1)
	return app, jobs, func(d time.Duration) { *clock = clock.Add(d) }
}

// TestHelperExec lets the test binary stand in for the hire executable.
func TestHelperExec(t *testing.T) {
	args := os.Args
	idx := -1
	for i, a := range args {
		if a == "--" {
			idx = i
		}
	}
	if idx < 0 {
		t.Skip("helper only")
	}
	rest := args[idx+1:]
	if len(rest) == 0 {
		t.Skip("helper only")
	}
	switch rest[0] {
	case "exec":
		os.Exit(runExec(rest[1:], os.Stdout, os.Stderr))
	case "ask":
		os.Exit(runAskShim(rest[1:], os.Stderr))
	}
	t.Skip("helper only")
}

func call(t *testing.T, handler http.Handler, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "http://127.0.0.1:8790"+path, reader)
	req.Header.Set("X-Hire-Token", "test-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var payload map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	return rec, payload
}

func TestSlugifyAndSummary(t *testing.T) {
	if got := slugify("  Release Notes Clerk! "); got != "release-notes-clerk" {
		t.Fatalf("slugify = %q", got)
	}
	if got := summaryLine("# Draft the weekly note\nmore"); got != "Draft the weekly note" {
		t.Fatalf("summaryLine = %q", got)
	}
	long := strings.Repeat("word ", 40)
	if got := summaryLine(long); len(got) > 90 || !strings.HasSuffix(got, "…") {
		t.Fatalf("summaryLine long = %q", got)
	}
}

func TestNextDue(t *testing.T) {
	loc := time.UTC
	after := time.Date(2026, 9, 4, 10, 30, 0, 0, loc) // a Friday
	cases := []struct {
		every, at string
		weekday   int
		want      time.Time
	}{
		{"hourly", "", 0, time.Date(2026, 9, 4, 11, 0, 0, 0, loc)},
		{"daily", "09:00", 0, time.Date(2026, 9, 5, 9, 0, 0, 0, loc)},
		{"daily", "11:00", 0, time.Date(2026, 9, 4, 11, 0, 0, 0, loc)},
		{"weekdays", "09:00", 0, time.Date(2026, 9, 7, 9, 0, 0, 0, loc)},
		{"weekly", "09:00", 3, time.Date(2026, 9, 9, 9, 0, 0, 0, loc)},
		{"15m", "", 0, time.Date(2026, 9, 4, 10, 45, 0, 0, loc)},
	}
	for _, c := range cases {
		if got := nextDue(c.every, c.at, c.weekday, after, loc); !got.Equal(c.want) {
			t.Errorf("nextDue(%s %s %d) = %s, want %s", c.every, c.at, c.weekday, got, c.want)
		}
	}
	if err := validateCadence("daily", "25:00", 0); err == nil {
		t.Fatal("expected invalid clock to be refused")
	}
	if err := validateCadence("10s", "", 0); err == nil {
		t.Fatal("expected sub-minute cadence to be refused")
	}
	if occurrenceID("r1", after) != occurrenceID("r1", after.Add(20*time.Second)) {
		t.Fatal("occurrence id must be stable within the minute")
	}
}

func TestCheckScriptDecidesDone(t *testing.T) {
	home := t.TempDir()
	if err := writeHomeFiles(home, Worker{Name: "W", Purpose: "p"}); err != nil {
		t.Fatal(err)
	}
	run := func() (int, string) {
		cmd := exec.Command("/bin/sh", filepath.Join(home, "bin", "check"))
		cmd.Dir = filepath.Join(home, "work")
		out, err := cmd.CombinedOutput()
		code := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return code, string(out)
	}
	if code, out := run(); code != 1 || !strings.Contains(out, "no current request") {
		t.Fatalf("empty home: %d %q", code, out)
	}
	req := Request{ID: "req-1", Kind: "request", Title: "t", Text: "do it", Check: "test -f done.txt", CreatedAt: time.Now()}
	if err := os.WriteFile(filepath.Join(home, "REQUEST.md"), []byte(renderRequestFile(req)), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := run(); code != 1 || !strings.Contains(out, "missing work/requests/req-1/RESULT.md") {
		t.Fatalf("no result: %d %q", code, out)
	}
	if err := os.MkdirAll(filepath.Join(home, "work", "requests", "req-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "work", "requests", "req-1", "RESULT.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := run(); code != 1 {
		t.Fatal("an empty RESULT.md must not count")
	}
	if err := os.WriteFile(filepath.Join(home, "work", "requests", "req-1", "RESULT.md"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := run(); code != 1 || !strings.Contains(out, "request check failed") {
		t.Fatalf("request check should fail before done.txt exists: %d %q", code, out)
	}
	if err := os.WriteFile(filepath.Join(home, "work", "done.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := run(); code != 0 {
		t.Fatalf("expected acceptance: %d %q", code, out)
	}
}

func TestFileBoundary(t *testing.T) {
	home := t.TempDir()
	if err := writeHomeFiles(home, Worker{Name: "W", Purpose: "p"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../x", "/etc/passwd", "work/../GOAL.md"} {
		if _, err := cleanRelative(bad); err == nil {
			t.Errorf("cleanRelative(%q) accepted", bad)
		}
	}
	if err := writeHomeFile(home, "GOAL.md", "x"); err == nil {
		t.Fatal("definition files must not be writable through the file API")
	}
	if err := writeHomeFile(home, "work/notes.md", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(home, "work", "escape")); err == nil {
		if _, err := browseHome(home, "work/escape"); err == nil {
			t.Fatal("symlink must be refused")
		}
	}
	view, err := browseHome(home, "work")
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range view.Entries {
		names = append(names, e.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "notes.md") || strings.Contains(strings.Join(names, ","), "escape") {
		t.Fatalf("listing = %v", names)
	}
	if _, err := browseHome(home, ".agent"); err == nil {
		t.Fatal(".agent must not be browsable")
	}
}

func TestAttemptsFromTendEvents(t *testing.T) {
	raw := `{"job":"p","seq":2,"kind":"attempt.prepared","created_us":1788435872755650,"payload":{"attempt":"attempt-1","number":1}}
{"job":"p","seq":3,"kind":"attempt.started","created_us":1788435872771890,"payload":{"attempt":"attempt-1","process_pid":95201}}
{"job":"p","seq":4,"kind":"attempt.finished","created_us":1788435872794796,"payload":{"attempt":"attempt-1","exit":2,"note":"observed exit 2","status":"failed"}}`
	var events []JobEvent
	for _, line := range strings.Split(raw, "\n") {
		var e JobEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	attempts := attemptsFromEvents(events, nil)
	if len(attempts) != 1 || attempts[0].Number != 1 || attempts[0].Status != "failed" || attempts[0].Exit == nil || *attempts[0].Exit != 2 {
		t.Fatalf("attempts = %+v", attempts)
	}
	two := 2
	if got := deriveState(Request{Runs: true}, &Job{Status: "failed"}, &two, time.Now()); got != "unfinished" {
		t.Fatalf("exit 2 should read as unfinished, got %s", got)
	}
	if got := deriveState(Request{Runs: true}, &Job{Status: "ready", NotBeforeUS: time.Now().Add(time.Hour).UnixMicro()}, nil, time.Now()); got != "scheduled" {
		t.Fatalf("future ready should read as scheduled, got %s", got)
	}
	if got := deriveState(Request{Runs: false}, nil, nil, time.Now()); got != "planned" {
		t.Fatalf("parent should read as planned, got %s", got)
	}
}

func TestGuardRequiresToken(t *testing.T) {
	app, _, _ := newTestApp(t)
	handler := app.routes()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8790/api/workers", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing token should be refused, got %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "http://evil.example:8790/api/bootstrap", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("foreign host should be refused, got %d", rec.Code)
	}
}

func TestCreateWorkerRequestAndRun(t *testing.T) {
	app, jobs, _ := newTestApp(t)
	handler := app.routes()
	ctx := context.Background()

	rec, payload := call(t, handler, http.MethodPost, "/api/workers", createWorkerRequest{Name: "Release Clerk", Purpose: "Write release notes."})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	worker := payload["worker"].(map[string]any)
	if worker["enabled"] != true || worker["checkState"] != "valid" {
		t.Fatalf("worker should be deployed: %v", worker)
	}
	receipt := worker["receipt"].(map[string]any)
	if receipt["definitionSha256"] != "def123" || receipt["checkSha256"] != "chk789" {
		t.Fatalf("receipt digests missing: %v", receipt)
	}
	home := app.homeDir("release-clerk")
	for _, name := range []string{"GOAL.md", "AGENTS.md", "CHECKS.json", "bin/check"} {
		if _, err := os.Stat(filepath.Join(home, name)); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
	goal, _ := os.ReadFile(filepath.Join(home, "GOAL.md"))
	if !strings.Contains(string(goal), "Write release notes.") {
		t.Fatal("GOAL.md should carry the purpose")
	}

	rec, payload = call(t, handler, http.MethodPost, "/api/workers/release-clerk/requests", intakeRequest{Text: "Draft this week's note.\nKeep it short."})
	if rec.Code != http.StatusCreated {
		t.Fatalf("intake: %d %s", rec.Code, rec.Body.String())
	}
	request := payload["request"].(map[string]any)
	id := request["id"].(string)
	if request["title"] != "Draft this week's note." {
		t.Fatalf("title = %v", request["title"])
	}
	if list, _ := jobs.List(ctx); len(list) != 1 || list[0].ID != id || list[0].Status != "ready" {
		t.Fatalf("job not queued: %+v", list)
	}
	rec, payload = call(t, handler, http.MethodGet, "/api/workers/release-clerk/requests/"+id, nil)
	if rec.Code != 200 || payload["request"].(map[string]any)["state"] != "queued" {
		t.Fatalf("queued view: %d %v", rec.Code, payload["request"])
	}

	code, err := jobs.Work(ctx)
	if err != nil || code != 0 {
		t.Fatalf("work: %d %v", code, err)
	}
	rec, payload = call(t, handler, http.MethodGet, "/api/workers/release-clerk/requests/"+id, nil)
	view := payload["request"].(map[string]any)
	if view["state"] != "done" || view["resultExists"] != true {
		t.Fatalf("expected done with result: %v\nattempts=%v", view, payload["attempts"])
	}
	attempts := payload["attempts"].([]any)
	if len(attempts) != 1 || !strings.Contains(attempts[0].(map[string]any)["stderr"].(string), "fake run for "+id) {
		t.Fatalf("attempt evidence missing: %v", attempts)
	}
	if !strings.Contains(payload["result"].(map[string]any)["content"].(string), "Summary line for "+id) {
		t.Fatal("RESULT.md should be shown")
	}
	requestFile, _ := os.ReadFile(filepath.Join(home, "REQUEST.md"))
	if !strings.Contains(string(requestFile), "id: "+id) || !strings.Contains(string(requestFile), "Keep it short.") {
		t.Fatalf("REQUEST.md = %q", requestFile)
	}
	argv := payload["argv"].([]any)
	joined := make([]string, 0, len(argv))
	for _, a := range argv {
		joined = append(joined, a.(string))
	}
	if !strings.Contains(strings.Join(joined, " "), "-checkpoint "+id+" "+home+" --") || strings.Contains(strings.Join(joined, " "), "-net") {
		t.Fatalf("argv = %v", joined)
	}

	// A request whose worker never writes RESULT.md ends unfinished (exit 2),
	// shows up under attention, and can be retried on the same checkpoint.
	rec, payload = call(t, handler, http.MethodPost, "/api/workers/release-clerk/requests", intakeRequest{Text: "NOWRITE this one"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("intake 2: %d %s", rec.Code, rec.Body.String())
	}
	id2 := payload["request"].(map[string]any)["id"].(string)
	if code, err := jobs.Work(ctx); err != nil || code != 0 {
		t.Fatalf("work 2: %d %v", code, err)
	}
	rec, payload = call(t, handler, http.MethodGet, "/api/bootstrap", nil)
	attention := payload["attention"].([]any)
	if len(attention) != 1 || attention[0].(map[string]any)["state"] != "unfinished" {
		t.Fatalf("attention = %v", attention)
	}
	rec, _ = call(t, handler, http.MethodPost, "/api/workers/release-clerk/requests/"+id2+"/retry", nil)
	if rec.Code != 200 {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
	if job, _ := jobs.Show(ctx, id2); job.Status != "ready" {
		t.Fatalf("retry should requeue, got %s", job.Status)
	}
	jobs.MarkUnknown(id2)
	rec, payload = call(t, handler, http.MethodGet, "/api/workers/release-clerk/requests/"+id2, nil)
	if payload["request"].(map[string]any)["state"] != "unknown" {
		t.Fatal("unknown should surface")
	}
	rec, _ = call(t, handler, http.MethodPost, "/api/workers/release-clerk/requests/"+id2+"/resolve", map[string]string{"decision": "fail"})
	if rec.Code != 200 {
		t.Fatalf("resolve: %d %s", rec.Code, rec.Body.String())
	}

	// Definition edits re-run agent check; a rejected home pauses intake.
	rec, payload = call(t, handler, http.MethodPut, "/api/workers/release-clerk/definition", map[string]string{"name": "GOAL.md", "content": "# BREAK\n"})
	if rec.Code != 200 || payload["receipt"].(map[string]any)["valid"] != false {
		t.Fatalf("definition edit: %d %v", rec.Code, payload)
	}
	rec, _ = call(t, handler, http.MethodPost, "/api/workers/release-clerk/requests", intakeRequest{Text: "should refuse"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("intake on an invalid home should refuse, got %d", rec.Code)
	}
	rec, payload = call(t, handler, http.MethodPut, "/api/workers/release-clerk/definition", map[string]string{"name": "GOAL.md", "content": "# Outcome fixed\n"})
	if rec.Code != 200 || payload["receipt"].(map[string]any)["valid"] != true {
		t.Fatalf("definition fix: %d %v", rec.Code, payload)
	}

	// Files: write under work/, refuse elsewhere.
	rec, _ = call(t, handler, http.MethodPut, "/api/workers/release-clerk/files", map[string]string{"path": "state/kv/owner", "content": "patrick"})
	if rec.Code != 200 {
		t.Fatalf("write state: %d %s", rec.Code, rec.Body.String())
	}
	rec, _ = call(t, handler, http.MethodPut, "/api/workers/release-clerk/files", map[string]string{"path": "bin/check", "content": "exit 0"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bin/check must not be writable: %d", rec.Code)
	}
}

func TestStructuredWorkerChecksCompileAndRun(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	id := "req-20260903-120000-abcd"
	resultDir := filepath.Join(home, "work", "requests", id)
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "REQUEST.md"), []byte("id: "+id+"\ncheck: \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resultDir, "RESULT.md"), []byte("Summary\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(home, "work", "reports", "final note.txt")
	if err := os.MkdirAll(filepath.Dir(report), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte("It's ready; $(nothing runs)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checks := []WorkerCheck{
		{Kind: "file_nonempty", Path: "reports/final note.txt", Description: "The final report exists"},
		{Kind: "text_contains", Path: "reports/final note.txt", Text: "It's ready; $(nothing runs)", Description: "The report carries the required marker"},
		{Kind: "minimum_bytes", Path: "requests/{request_id}/RESULT.md", MinimumBytes: 10, Description: "The result has substance"},
	}
	if err := writeWorkerChecks(home, checks); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(home, "bin", "check"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compiled check should pass: %v: %s", err, output)
	}
	if err := os.WriteFile(report, []byte("wrong\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(filepath.Join(home, "bin", "check"))
	if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "required marker") {
		t.Fatalf("compiled check should explain its rejection: %v: %s", err, output)
	}
	doc, err := readWorkerChecks(home)
	if err != nil || len(doc) != 3 || doc[2].Path != "requests/{request_id}/RESULT.md" {
		t.Fatalf("CHECKS.json did not round trip: %+v, %v", doc, err)
	}
}

func TestWorkerCheckValidation(t *testing.T) {
	valid, err := normalizeWorkerChecks([]WorkerCheck{{Kind: "file_nonempty", Path: "work/reports/latest.md", Description: "Latest report exists"}})
	if err != nil || len(valid) != 1 || valid[0].Path != "reports/latest.md" {
		t.Fatalf("normalize valid check: %+v, %v", valid, err)
	}
	bad := []WorkerCheck{
		{Kind: "file_nonempty", Path: "../secret", Description: "escape"},
		{Kind: "shell", Path: "report.md", Description: "arbitrary command"},
		{Kind: "text_contains", Path: "report.md", Description: "missing text"},
		{Kind: "minimum_bytes", Path: "report.md", Description: "bad size", MinimumBytes: 0},
		{Kind: "file_nonempty", Path: "reports/{today}.md", Description: "unknown placeholder"},
		{Kind: "file_nonempty", Path: "reports/latest.md\nexit 0", Description: "multiline path"},
	}
	for _, check := range bad {
		if _, err := normalizeWorkerChecks([]WorkerCheck{check}); err == nil {
			t.Errorf("accepted invalid check: %+v", check)
		}
	}
}

func TestAIAssistedWorkerChecks(t *testing.T) {
	app, jobs, _ := newTestApp(t)
	handler := app.routes()
	rec, _ := call(t, handler, http.MethodPost, "/api/workers", createWorkerRequest{Name: "Release Clerk", Purpose: "Write work/release-notes/latest.md with a Release notes heading."})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if err := app.store.SaveModelProof(ModelProof{Model: "openai/fake-model", OK: true, Output: "ok", At: app.now()}); err != nil {
		t.Fatal(err)
	}
	reply := filepath.Join(t.TempDir(), "checks.json")
	response := `{"checks":[{"kind":"text_contains","path":"requests/{request_id}/RESULT.md","description":"The result names the required heading","text":"body","minimumBytes":0}],"note":"The requested heading is a stable textual condition."}`
	if err := os.WriteFile(reply, []byte(response), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_ASK_REPLY", reply)
	rec, payload := call(t, handler, http.MethodPost, "/api/workers/release-clerk/checks/suggest", map[string]string{"guidance": "Make sure the result has the required content."})
	if rec.Code != http.StatusOK {
		t.Fatalf("suggest: %d %s", rec.Code, rec.Body.String())
	}
	if payload["model"] != "openai/fake-model" || len(payload["checks"].([]any)) != 1 {
		t.Fatalf("suggestion = %v", payload)
	}
	beforeApply, err := readWorkerChecks(app.homeDir("release-clerk"))
	if err != nil || len(beforeApply) != 0 {
		t.Fatalf("AI suggestions must not apply themselves: %+v, %v", beforeApply, err)
	}
	checks := []WorkerCheck{{Kind: "text_contains", Path: "requests/{request_id}/RESULT.md", Description: "The result names the required heading", Text: "body"}}
	rec, payload = call(t, handler, http.MethodPut, "/api/workers/release-clerk/checks", map[string]any{"checks": checks})
	if rec.Code != http.StatusOK || payload["receipt"].(map[string]any)["valid"] != true {
		t.Fatalf("apply checks: %d %v", rec.Code, payload)
	}
	rec, payload = call(t, handler, http.MethodGet, "/api/workers/release-clerk/definition", nil)
	if rec.Code != http.StatusOK || len(payload["checks"].([]any)) != 1 || payload["checkAssistant"].(map[string]any)["ready"] != true {
		t.Fatalf("definition checks = %d %v", rec.Code, payload)
	}
	script, err := os.ReadFile(filepath.Join(app.homeDir("release-clerk"), "bin", "check"))
	if err != nil || !strings.Contains(string(script), "grep -F -q") {
		t.Fatalf("compiled script = %q, %v", script, err)
	}
	rec, payload = call(t, handler, http.MethodPost, "/api/workers/release-clerk/requests", intakeRequest{Text: "Write the note."})
	if rec.Code != http.StatusCreated {
		t.Fatalf("intake: %d %s", rec.Code, rec.Body.String())
	}
	if code, err := jobs.Work(context.Background()); err != nil || code != 0 {
		t.Fatalf("work: %d %v", code, err)
	}
	id := payload["request"].(map[string]any)["id"].(string)
	rec, payload = call(t, handler, http.MethodGet, "/api/workers/release-clerk/requests/"+id, nil)
	if rec.Code != http.StatusOK || payload["request"].(map[string]any)["state"] != "done" {
		t.Fatalf("checked request = %d %v", rec.Code, payload)
	}
	rec, _ = call(t, handler, http.MethodPut, "/api/workers/release-clerk/checks", map[string]any{"checks": []WorkerCheck{{Kind: "file_nonempty", Path: "../outside", Description: "escape"}}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsafe check should be rejected, got %d", rec.Code)
	}
}

func TestPlannedActionsAndRoutines(t *testing.T) {
	app, jobs, advance := newTestApp(t)
	handler := app.routes()
	ctx := context.Background()
	call(t, handler, http.MethodPost, "/api/workers", createWorkerRequest{Name: "Planner", Purpose: "Plan things."})
	reply := filepath.Join(t.TempDir(), "reply.json")
	if err := os.WriteFile(reply, []byte(`{"actions":[{"title":"Draft now","instructions":"Draft the outline.","when":"now","repeat":""},{"title":"Remind later","instructions":"Send the reminder.","when":"2026-09-04T09:00:00Z","repeat":""},{"title":"Weekly digest","instructions":"Write the digest.","when":"2026-09-07T09:00:00Z","repeat":"weekly"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_ASK_REPLY", reply)
	rec, payload := call(t, handler, http.MethodPost, "/api/workers/planner/requests", intakeRequest{Text: "Draft the outline, remind me tomorrow, and every Monday write a digest.", Plan: true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("planned intake: %d %s", rec.Code, rec.Body.String())
	}
	actions := payload["actions"].([]any)
	routines := payload["routines"].([]any)
	if len(actions) != 2 || len(routines) != 1 {
		t.Fatalf("actions=%d routines=%d: %v", len(actions), len(routines), payload)
	}
	list, _ := jobs.List(ctx)
	if len(list) != 2 {
		t.Fatalf("expected two jobs, got %d", len(list))
	}
	var later Job
	for _, job := range list {
		if strings.HasSuffix(job.ID, "-a2") {
			later = job
		}
	}
	if later.NotBeforeUS != time.Date(2026, 9, 4, 9, 0, 0, 0, time.UTC).UnixMicro() {
		t.Fatalf("later action should keep its time: %+v", later)
	}
	routine := routines[0].(map[string]any)
	if routine["every"] != "weekly" || routine["weekday"] != float64(1) || routine["at"] != "09:00" {
		t.Fatalf("routine = %v", routine)
	}
	parent := payload["request"].(map[string]any)
	rec, payload = call(t, handler, http.MethodGet, "/api/workers/planner/requests/"+parent["id"].(string), nil)
	if payload["request"].(map[string]any)["state"] != "planned" || len(payload["children"].([]any)) != 2 {
		t.Fatalf("parent view = %v", payload["request"])
	}
	plan := payload["plan"].(map[string]any)
	if plan["fallback"] != false || plan["model"] != "openai/fake-model" {
		t.Fatalf("plan = %v", plan)
	}

	// The scheduler queues one occurrence per due routine and never twice.
	advance(5 * 24 * time.Hour)
	if queued := app.runner.Tick(ctx, app.now()); queued != 1 {
		t.Fatalf("first tick queued %d", queued)
	}
	if queued := app.runner.Tick(ctx, app.now()); queued != 0 {
		t.Fatalf("second tick queued %d", queued)
	}
	saved, err := app.store.Routine("planner", routine["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if !saved.NextDue.After(app.now()) || saved.LastRequest == "" {
		t.Fatalf("routine did not advance: %+v", saved)
	}
	if req, err := app.store.Request("planner", saved.LastRequest); err != nil || req.Kind != "routine" || req.Text != "Write the digest." {
		t.Fatalf("routine request = %+v %v", req, err)
	}

	// Without a model reply the planner falls back to one action now.
	t.Setenv("FAKE_ASK_REPLY", "/nonexistent")
	rec, payload = call(t, handler, http.MethodPost, "/api/workers/planner/requests", intakeRequest{Text: "Just do it", Plan: true})
	if rec.Code != http.StatusCreated || len(payload["warnings"].([]any)) != 1 || payload["plan"].(map[string]any)["fallback"] != true {
		t.Fatalf("fallback intake: %d %v", rec.Code, payload)
	}
}

func TestWorkerAndRoutineUpdates(t *testing.T) {
	app, _, _ := newTestApp(t)
	handler := app.routes()
	call(t, handler, http.MethodPost, "/api/workers", createWorkerRequest{Name: "Weather", Purpose: "Old summary.", Routine: &routineInput{Instructions: "Use wttr.in", Every: "daily", At: "06:00"}})
	rec, payload := call(t, handler, http.MethodPost, "/api/workers/weather/update", map[string]any{"purpose": "New summary.", "model": "openai-codex/gpt-5.6-sol", "network": true})
	if rec.Code != 200 || payload["purpose"] != "New summary." || payload["model"] != "openai-codex/gpt-5.6-sol" || payload["network"] != true {
		t.Fatalf("update: %d %v", rec.Code, payload)
	}
	rec, _ = call(t, handler, http.MethodPost, "/api/workers/weather/update", map[string]any{"model": "codex/x"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad model should be refused, got %d", rec.Code)
	}
	rec, payload = call(t, handler, http.MethodGet, "/api/workers/weather", nil)
	routine := payload["routines"].([]any)[0].(map[string]any)
	rec, payload = call(t, handler, http.MethodPost, "/api/workers/weather/routines/"+routine["id"].(string)+"/update", routineInput{Instructions: "Use api.weather.gov", Every: "weekdays", At: "07:30"})
	if rec.Code != 200 || payload["instructions"] != "Use api.weather.gov" || payload["every"] != "weekdays" || payload["at"] != "07:30" {
		t.Fatalf("routine update: %d %v", rec.Code, payload)
	}
	next, _ := time.Parse(time.RFC3339, payload["nextDue"].(string))
	if !next.After(app.now()) || next.Weekday() == time.Saturday || next.Weekday() == time.Sunday {
		t.Fatalf("next due not rescheduled: %s", next)
	}
	rec, _ = call(t, handler, http.MethodPost, "/api/workers/weather/routines/"+routine["id"].(string)+"/update", routineInput{Instructions: "x", Every: "daily", At: "99:00"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad cadence should be refused, got %d", rec.Code)
	}
}

func writeTestReply(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExpertBuilderCreatesOnlyAfterExplicitApply(t *testing.T) {
	app, _, _ := newTestApp(t)
	handler := app.routes()
	if err := app.store.SaveModelProof(ModelProof{Model: "openai/fake-model", OK: true, Output: "ok", At: app.now()}); err != nil {
		t.Fatal(err)
	}
	replies := t.TempDir()
	t.Setenv("FAKE_BUILDER_ROUTER_REPLY", writeTestReply(t, replies, "router.json", `{"experts":[{"name":"Release operations lead","focus":"Review source precedence, ownership, and release exceptions.","reason":"The worker owns a release process."},{"name":"Technical editor","focus":"Review audience, structure, and citation expectations.","reason":"The deliverable is published prose."}]}`))
	t.Setenv("FAKE_BUILDER_EXPERT_REPLY", writeTestReply(t, replies, "expert.json", `{"summary":"The draft needs explicit source precedence and escalation.","recommendations":["Name the authoritative pull-request source."],"risks":["Ambiguous ownership can produce a misleading note."]}`))
	synthesis := `{"message":"The team drafted a complete release worker for your review.","ready":true,"changes":["Defined source precedence and escalation","Kept network off"],"definition":{"name":"Release Editor","purpose":"Draft an evidence-backed weekly release note.","network":false,"files":{"goal":"# Outcome\nDraft the weekly release note.\n\n## Done\nWrite the request result and a release note.\n","agents":"# Operating method\nRead REQUEST.md and the reviewed local pull-request evidence. Stop when ownership is unclear. Write work/requests/{request_id}/RESULT.md.\n","soul":"# Voice\nClear and factual.\n","plan":"","memory":"","heartbeat":""},"checks":[{"kind":"file_nonempty","path":"release-notes/latest.md","description":"A release-note artifact exists","text":"","minimumBytes":0}]}}`
	t.Setenv("FAKE_BUILDER_SYNTHESIS_REPLY", writeTestReply(t, replies, "synthesis.json", synthesis))
	logPath := filepath.Join(replies, "ask.log")
	t.Setenv("FAKE_ASK_LOG", logPath)

	rec, payload := call(t, handler, http.MethodPost, "/api/builder/chat", map[string]string{"message": "Build a weekly release notes worker from reviewed local PR evidence."})
	if rec.Code != http.StatusAccepted || payload["active"] != true {
		t.Fatalf("builder chat: %d %s", rec.Code, rec.Body.String())
	}
	payload = waitForBuilderTurn(t, handler, "")
	session := payload["session"].(map[string]any)
	if session["ready"] != true || len(session["reports"].([]any)) != 5 || len(session["experts"].([]any)) != 5 {
		t.Fatalf("builder session = %v", session)
	}
	if workers, err := app.store.Workers(); err != nil || len(workers) != 0 {
		t.Fatalf("proposal must not create a worker: %+v %v", workers, err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if strings.Count(logText, "builder-router-schema.json") != 1 || strings.Count(logText, "builder-expert-schema.json") != 5 || strings.Count(logText, "agent-builder-schema.json") != 1 {
		t.Fatalf("unexpected expert orchestration:\n%s", logText)
	}
	rec, payload = call(t, handler, http.MethodGet, "/api/builder", nil)
	if rec.Code != http.StatusOK || payload["session"].(map[string]any)["ready"] != true {
		t.Fatalf("builder resume: %d %v", rec.Code, payload)
	}
	rec, payload = call(t, handler, http.MethodPost, "/api/builder/apply", map[string]string{"workerSlug": ""})
	if rec.Code != http.StatusOK {
		t.Fatalf("builder apply: %d %s", rec.Code, rec.Body.String())
	}
	worker := payload["worker"].(map[string]any)
	if worker["slug"] != "release-editor" || worker["network"] != false || worker["checkState"] != "valid" {
		t.Fatalf("applied worker = %v", worker)
	}
	goal, err := os.ReadFile(filepath.Join(app.homeDir("release-editor"), "GOAL.md"))
	if err != nil || !strings.Contains(string(goal), "weekly release note") {
		t.Fatalf("GOAL.md = %q, %v", goal, err)
	}
	checks, err := readWorkerChecks(app.homeDir("release-editor"))
	if err != nil || len(checks) != 1 || checks[0].Path != "release-notes/latest.md" {
		t.Fatalf("checks = %+v, %v", checks, err)
	}
	if _, err := app.store.BuilderSession(""); !errors.Is(err, errNotFound) {
		t.Fatalf("current builder session should be archived, got %v", err)
	}
}

func TestBuilderProvesAnUnprovedModelItself(t *testing.T) {
	app, _, _ := newTestApp(t)
	handler := app.routes()
	rec, payload := call(t, handler, http.MethodGet, "/api/builder", nil)
	assistant := payload["assistant"].(map[string]any)
	if rec.Code != http.StatusOK || assistant["ready"] != true || assistant["proved"] != false {
		t.Fatalf("a configured but unproved model must not block the composer: %d %v", rec.Code, assistant)
	}
	// When the model cannot answer, the turn fails and says so in plain words.
	t.Setenv("FAKE_ASK_REPLY", filepath.Join(t.TempDir(), "missing-reply.txt"))
	rec, _ = call(t, handler, http.MethodPost, "/api/builder/chat", map[string]string{"message": "Build a worker."})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
	}
	turn := latestBuilderTurn(t, waitForBuilderTurn(t, handler, ""))
	if turn["status"] != "failed" || !strings.Contains(turn["error"].(string), "did not answer a test call") {
		t.Fatalf("a failed test call must fail the turn honestly: %v", turn)
	}
	if app.builderModelReady("openai/fake-model") {
		t.Fatal("a failed test call must not count as proof")
	}
	// With a working model the first turn proves it on the way through.
	replies := t.TempDir()
	t.Setenv("FAKE_ASK_REPLY", writeTestReply(t, replies, "ok.txt", "ok\n"))
	t.Setenv("FAKE_BUILDER_ROUTER_REPLY", writeTestReply(t, replies, "router.json", `{"experts":[{"name":"Editor","focus":"Structure.","reason":"Prose."}]}`))
	t.Setenv("FAKE_BUILDER_EXPERT_REPLY", writeTestReply(t, replies, "expert.json", `{"summary":"Fine.","recommendations":[],"risks":[]}`))
	t.Setenv("FAKE_BUILDER_SYNTHESIS_REPLY", writeTestReply(t, replies, "synthesis.json", `{"message":"Ready.","ready":true,"changes":[],"definition":{"name":"Notes","purpose":"Write notes.","network":false,"files":{"goal":"# Outcome\nNotes.\n","agents":"# Method\nWrite work/requests/{request_id}/RESULT.md.\n","soul":"","plan":"","memory":"","heartbeat":""},"checks":[]}}`))
	rec, _ = call(t, handler, http.MethodPost, "/api/builder/chat", map[string]string{"message": "Build a notes worker."})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("second chat: %d %s", rec.Code, rec.Body.String())
	}
	if turn := latestBuilderTurn(t, waitForBuilderTurn(t, handler, "")); turn["status"] != "complete" {
		t.Fatalf("turn with a working model: %v", turn)
	}
	if !app.builderModelReady("openai/fake-model") {
		t.Fatal("the first successful turn must leave the model proved")
	}
	rec, payload = call(t, handler, http.MethodGet, "/api/builder", nil)
	if rec.Code != http.StatusOK || payload["assistant"].(map[string]any)["proved"] != true {
		t.Fatalf("proved flag = %v", payload["assistant"])
	}
}

func TestExpertBuilderExplainsExactModelProofMismatch(t *testing.T) {
	app, _, _ := newTestApp(t)
	handler := app.routes()
	rec, _ := call(t, handler, http.MethodPost, "/api/workers", createWorkerRequest{
		Name: "Weather", Purpose: "Build an animated weather report.", Model: "openai-codex/gpt-5.6-luna",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create worker: %d %s", rec.Code, rec.Body.String())
	}
	if err := app.store.SaveModelProof(ModelProof{Model: "openai-codex/gpt-5.6-sol", OK: true, Output: "ok", At: app.now()}); err != nil {
		t.Fatal(err)
	}

	rec, payload := call(t, handler, http.MethodGet, "/api/builder?worker=weather", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("builder: %d %s", rec.Code, rec.Body.String())
	}
	assistant := payload["assistant"].(map[string]any)
	if assistant["ready"] != true || assistant["proved"] != false || assistant["model"] != "openai-codex/gpt-5.6-luna" || assistant["message"] != nil {
		t.Fatalf("a worker on a different, unproved model must still be ready with no nagging: %#v", assistant)
	}
	// The first turn proves luna itself, and sol stays proved beside it.
	replies := t.TempDir()
	t.Setenv("FAKE_ASK_REPLY", writeTestReply(t, replies, "ok.txt", "ok\n"))
	t.Setenv("FAKE_BUILDER_ROUTER_REPLY", writeTestReply(t, replies, "router.json", `{"experts":[{"name":"Meteorologist","focus":"Forecast sources.","reason":"Weather."}]}`))
	t.Setenv("FAKE_BUILDER_EXPERT_REPLY", writeTestReply(t, replies, "expert.json", `{"summary":"Fine.","recommendations":[],"risks":[]}`))
	t.Setenv("FAKE_BUILDER_SYNTHESIS_REPLY", writeTestReply(t, replies, "synthesis.json", `{"message":"Ready.","ready":true,"changes":[],"definition":{"name":"Weather","purpose":"Report weather.","network":false,"files":{"goal":"# Outcome\nReport.\n","agents":"# Method\nWrite work/requests/{request_id}/RESULT.md.\n","soul":"","plan":"","memory":"","heartbeat":""},"checks":[]}}`))
	rec, _ = call(t, handler, http.MethodPost, "/api/builder/chat", map[string]string{"workerSlug": "weather", "message": "Make it snarkier."})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
	}
	if turn := latestBuilderTurn(t, waitForBuilderTurn(t, handler, "weather")); turn["status"] != "complete" {
		t.Fatalf("turn: %v", turn)
	}
	for _, model := range []string{"openai-codex/gpt-5.6-luna", "openai-codex/gpt-5.6-sol"} {
		if !app.builderModelReady(model) {
			t.Fatalf("%s should be proved", model)
		}
	}
}

func TestStaleDraftAsksToStartOverNotToProve(t *testing.T) {
	app, _, _ := newTestApp(t)
	handler := app.routes()
	rec, _ := call(t, handler, http.MethodPost, "/api/workers", createWorkerRequest{Name: "Notes", Purpose: "Write notes."})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if err := app.store.SaveModelProof(ModelProof{Model: "openai/fake-model", OK: true, Output: "ok", At: app.now()}); err != nil {
		t.Fatal(err)
	}
	replies := t.TempDir()
	t.Setenv("FAKE_BUILDER_ROUTER_REPLY", writeTestReply(t, replies, "router.json", `{"experts":[{"name":"Editor","focus":"Structure.","reason":"Prose."}]}`))
	t.Setenv("FAKE_BUILDER_EXPERT_REPLY", writeTestReply(t, replies, "expert.json", `{"summary":"Fine.","recommendations":[],"risks":[]}`))
	t.Setenv("FAKE_BUILDER_SYNTHESIS_REPLY", writeTestReply(t, replies, "synthesis.json", `{"message":"Ready.","ready":true,"changes":[],"definition":{"name":"Notes","purpose":"Write notes.","network":false,"files":{"goal":"# Outcome\nNotes.\n","agents":"# Method\nWrite work/requests/{request_id}/RESULT.md.\n","soul":"","plan":"","memory":"","heartbeat":""},"checks":[]}}`))
	rec, _ = call(t, handler, http.MethodPost, "/api/builder/chat", map[string]string{"workerSlug": "notes", "message": "Draft it."})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
	}
	waitForBuilderTurn(t, handler, "notes")
	rec, _ = call(t, handler, http.MethodPut, "/api/workers/notes/definition", map[string]string{"name": "GOAL.md", "content": "# Outcome\nEdited by hand meanwhile.\n"})
	if rec.Code != http.StatusOK {
		t.Fatalf("manual edit: %d %s", rec.Code, rec.Body.String())
	}
	rec, payload := call(t, handler, http.MethodGet, "/api/builder?worker=notes", nil)
	assistant := payload["assistant"].(map[string]any)
	message := strings.ToLower(fmt.Sprint(assistant["message"]))
	if rec.Code != http.StatusOK || assistant["ready"] != true || assistant["stale"] != "definition" || !strings.Contains(message, "continues from the current files") || strings.Contains(message, "prove") {
		t.Fatalf("a stale draft must keep the composer open, explain the rebase, and say nothing about proving: %#v", assistant)
	}
	// The set-aside proposal cannot be applied over the hand edit...
	if rec, _ = call(t, handler, http.MethodPost, "/api/builder/apply", map[string]string{"workerSlug": "notes"}); rec.Code != http.StatusConflict {
		t.Fatalf("applying a stale proposal must be refused, got %d", rec.Code)
	}
	// ...but the next message continues from the current definition and the conversation.
	rec, _ = call(t, handler, http.MethodPost, "/api/builder/chat", map[string]string{"workerSlug": "notes", "message": "Keep going."})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("chat after a hand edit: %d %s", rec.Code, rec.Body.String())
	}
	payload = waitForBuilderTurn(t, handler, "notes")
	session := payload["session"].(map[string]any)
	if latestBuilderTurn(t, payload)["status"] != "complete" || payload["assistant"].(map[string]any)["stale"] != nil {
		t.Fatalf("the rebased turn must complete and clear the stale flag: %v", payload["assistant"])
	}
	var noted bool
	for _, item := range session["messages"].([]any) {
		if m := item.(map[string]any); m["role"] == "note" && strings.Contains(m["text"].(string), "edited outside this conversation") {
			noted = true
		}
	}
	if !noted {
		t.Fatalf("the conversation must record why it was rebased: %v", session["messages"])
	}
	if rec, _ = call(t, handler, http.MethodPost, "/api/builder/apply", map[string]string{"workerSlug": "notes"}); rec.Code != http.StatusOK {
		t.Fatalf("apply after the rebased turn: %d %s", rec.Code, rec.Body.String())
	}
}

func TestEditWorkerShowsDirectEditorBeforeOptionalBuilder(t *testing.T) {
	source, err := webAssets.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "async function renderDefinitionTab")
	if start < 0 {
		t.Fatal("definition tab source not found")
	}
	endOffset := strings.Index(text[start:], "async function renderHistoryTab")
	if endOffset < 0 {
		t.Fatal("definition tab source not found")
	}
	definitionTab := text[start : start+endOffset]
	editor := strings.Index(definitionTab, `<section class="direct-editor"`)
	builder := strings.Index(definitionTab, `<details class="expert-builder-disclosure"`)
	if editor < 0 || builder < 0 || editor > builder {
		t.Fatalf("direct editor must appear before optional expert builder: editor=%d builder=%d", editor, builder)
	}
	for _, expected := range []string{"Editable now", "Save and check", "builder-reset", "Draft continues from current files"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("web editor missing %q", expected)
		}
	}
}

func TestBuilderDefinitionHonorsAgentSizeLimits(t *testing.T) {
	def := AgentDefinition{Name: "Large", Purpose: "Too large", Files: AgentDefinitionFiles{Goal: "# Goal\n", Agents: strings.Repeat("a", 32*1024+1)}}
	if _, err := normalizeAgentDefinition(def, true); err == nil || !strings.Contains(err.Error(), "32 KiB") {
		t.Fatalf("expected per-file Agent limit, got %v", err)
	}
	def.Files.Agents = strings.Repeat("a", 32*1024)
	def.Files.Goal = strings.Repeat("g", 32*1024)
	if _, err := normalizeAgentDefinition(def, true); err == nil || !strings.Contains(err.Error(), "combined 64 KiB") {
		t.Fatalf("expected combined Agent limit after normalization, got %v", err)
	}
}

func TestExpertBuilderFailureKeepsLastProposal(t *testing.T) {
	app, _, _ := newTestApp(t)
	handler := app.routes()
	if err := app.store.SaveModelProof(ModelProof{Model: "openai/fake-model", OK: true, Output: "ok", At: app.now()}); err != nil {
		t.Fatal(err)
	}
	replies := t.TempDir()
	t.Setenv("FAKE_BUILDER_ROUTER_REPLY", writeTestReply(t, replies, "router.json", `{"experts":[{"name":"Operations lead","focus":"Review the workflow.","reason":"The task is operational."}]}`))
	t.Setenv("FAKE_BUILDER_EXPERT_REPLY", writeTestReply(t, replies, "expert.json", `{"summary":"Review complete.","recommendations":[],"risks":[]}`))
	first := `{"message":"One decision is still needed.","ready":false,"changes":["Drafted the workflow"],"definition":{"name":"Inbox Clerk","purpose":"Triage an inbox.","network":false,"files":{"goal":"# Outcome\nTriage the inbox.\n","agents":"# Method\nRead REQUEST.md and write work/requests/{request_id}/RESULT.md.\n","soul":"","plan":"","memory":"","heartbeat":""},"checks":[]}}`
	t.Setenv("FAKE_BUILDER_SYNTHESIS_REPLY", writeTestReply(t, replies, "first.json", first))
	rec, _ := call(t, handler, http.MethodPost, "/api/builder/chat", map[string]string{"message": "Build an inbox clerk."})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("first chat: %d %s", rec.Code, rec.Body.String())
	}
	waitForBuilderTurn(t, handler, "")
	before, err := app.store.BuilderSession("")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_BUILDER_SYNTHESIS_REPLY", filepath.Join(replies, "missing.json"))
	rec, _ = call(t, handler, http.MethodPost, "/api/builder/chat", map[string]string{"message": "Use the shared support inbox."})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("second chat: %d %s", rec.Code, rec.Body.String())
	}
	waitForBuilderTurn(t, handler, "")
	after, err := app.store.BuilderSession("")
	if err != nil {
		t.Fatal(err)
	}
	if after.Proposal == nil || before.Proposal == nil || agentDefinitionSHA256(*after.Proposal) != agentDefinitionSHA256(*before.Proposal) || len(after.Messages) != len(before.Messages) {
		t.Fatalf("failed turn changed the accepted conversation/proposal: before=%+v after=%+v", before, after)
	}
	if len(after.Turns) != 2 || after.Turns[1].Status != "failed" || after.Turns[1].Error == "" {
		t.Fatalf("failed turn evidence missing: %+v", after.Turns)
	}
}

func TestExpertBuilderEditsExistingWorkerWithoutChangingIdentityOrSchedule(t *testing.T) {
	app, _, _ := newTestApp(t)
	handler := app.routes()
	rec, _ := call(t, handler, http.MethodPost, "/api/workers", createWorkerRequest{Name: "Release Clerk", Purpose: "Old purpose.", Routine: &routineInput{Instructions: "Draft weekly", Every: "weekly", At: "09:00", Weekday: 1}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if err := app.store.SaveModelProof(ModelProof{Model: "openai/fake-model", OK: true, Output: "ok", At: app.now()}); err != nil {
		t.Fatal(err)
	}
	replies := t.TempDir()
	t.Setenv("FAKE_BUILDER_ROUTER_REPLY", writeTestReply(t, replies, "router.json", `{"experts":[{"name":"Release manager","focus":"Review the release workflow.","reason":"This is a release role."}]}`))
	t.Setenv("FAKE_BUILDER_EXPERT_REPLY", writeTestReply(t, replies, "expert.json", `{"summary":"The revised workflow is bounded.","recommendations":[],"risks":[]}`))
	update := `{"message":"The definition is ready to review.","ready":true,"changes":["Clarified the workflow","Proposed network access"],"definition":{"name":"Release Curator","purpose":"Curate weekly release notes.","network":true,"files":{"goal":"# Outcome\nCurate the weekly release note.\n","agents":"# Method\nRead REQUEST.md, collect the named remote sources, and write work/requests/{request_id}/RESULT.md. Stop before publishing.\n","soul":"","plan":"","memory":"","heartbeat":""},"checks":[]}}`
	t.Setenv("FAKE_BUILDER_SYNTHESIS_REPLY", writeTestReply(t, replies, "update.json", update))
	rec, _ = call(t, handler, http.MethodPost, "/api/builder/chat", map[string]string{"workerSlug": "release-clerk", "message": "Refine this worker and let it read remote release sources."})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
	}
	waitForBuilderTurn(t, handler, "release-clerk")
	rec, payload := call(t, handler, http.MethodPost, "/api/builder/apply", map[string]string{"workerSlug": "release-clerk"})
	if rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body.String())
	}
	worker := payload["worker"].(map[string]any)
	if worker["slug"] != "release-clerk" || worker["name"] != "Release Curator" || worker["model"] != "openai/fake-model" || worker["network"] != true {
		t.Fatalf("updated worker = %v", worker)
	}
	routines, err := app.store.Routines("release-clerk")
	if err != nil || len(routines) != 1 || routines[0].Instructions != "Draft weekly" {
		t.Fatalf("routine changed: %+v %v", routines, err)
	}
}

func TestPlatformReviewersReceiveSuiteContractAndLiveHome(t *testing.T) {
	app, _, _ := newTestApp(t)
	handler := app.routes()
	rec, _ := call(t, handler, http.MethodPost, "/api/workers", createWorkerRequest{Name: "Release Clerk", Purpose: "Draft release notes.", Routine: &routineInput{Instructions: "Draft the weekly note", Every: "weekly", At: "09:00", Weekday: 1}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	home := app.homeDir("release-clerk")
	for _, dir := range []string{filepath.Join(home, "skills", "release-style"), filepath.Join(home, "agents"), filepath.Join(home, "state", "kv")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "tools", "pr-list"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state", "kv", "product-owner"), []byte("ops\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.store.SaveModelProof(ModelProof{Model: "openai/fake-model", OK: true, Output: "ok", At: app.now()}); err != nil {
		t.Fatal(err)
	}
	replies := t.TempDir()
	t.Setenv("FAKE_BUILDER_ROUTER_REPLY", writeTestReply(t, replies, "router.json", `{"experts":[{"name":"Release manager","focus":"Review the release workflow.","reason":"This is a release role."}]}`))
	t.Setenv("FAKE_BUILDER_EXPERT_REPLY", writeTestReply(t, replies, "expert.json", `{"summary":"The definition ignores durable state.","recommendations":["Name the state/kv keys the worker maintains."],"risks":[],"questions":["Which product owner decides release exceptions?"],"platformFit":[{"feature":"state_kv","status":"missing","note":"No state key scheme is named."},{"feature":"network","status":"not_needed","note":"Sources are local."},{"feature":"state_kv","status":"used","note":"duplicate that must be ignored"}]}`))
	t.Setenv("FAKE_BUILDER_SYNTHESIS_REPLY", writeTestReply(t, replies, "synthesis.json", `{"message":"Ready.","ready":true,"changes":["Named state keys"],"definition":{"name":"Release Clerk","purpose":"Draft release notes.","network":false,"files":{"goal":"# Outcome\nDraft the note.\n","agents":"# Method\nRead REQUEST.md, keep product owners under state/kv/, write work/requests/{request_id}/RESULT.md.\n","soul":"","plan":"","memory":"","heartbeat":""},"checks":[]}}`))
	inputLog := filepath.Join(replies, "input.log")
	t.Setenv("FAKE_ASK_INPUT_LOG", inputLog)

	rec, payload := call(t, handler, http.MethodPost, "/api/builder/chat", map[string]string{"workerSlug": "release-clerk", "message": "Make this worker remember product owners."})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
	}
	payload = waitForBuilderTurn(t, handler, "release-clerk")
	logData, err := os.ReadFile(inputLog)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if got := strings.Count(logText, `"reviewContract":`); got != 3 {
		t.Fatalf("exactly the three permanent reviewers must receive a checklist, got %d:\n%s", got, logText)
	}
	for _, expected := range []string{`"agentContract":`, `"hireContract":`, "32768 bytes", `"compiledCheck":`, "def123", "Draft the weekly note", "release-style", "pr-list", "product-owner", `"suite":{"agent":"agent fake"`, `"features":[{"id":"goal"`} {
		if !strings.Contains(logText, expected) {
			t.Fatalf("platform dossier missing %q:\n%s", expected, logText)
		}
	}
	calls := strings.Split(logText, "=== ")
	var routerCalls, synthesisCalls int
	for _, section := range calls {
		switch {
		case strings.Contains(section, "builder-router-schema.json"):
			routerCalls++
			if strings.Contains(section, `"platform":`) {
				t.Fatalf("the router should not carry the platform dossier:\n%s", section)
			}
		case strings.Contains(section, "agent-builder-schema.json"):
			synthesisCalls++
			if !strings.Contains(section, `"platform":`) || !strings.Contains(section, `"platformFit":[{"feature":"state_kv","status":"missing"`) || !strings.Contains(section, `"questions":["Which product owner decides release exceptions?"]`) {
				t.Fatalf("the lead must receive the dossier, platform-fit findings, and questions:\n%s", section)
			}
		}
	}
	if routerCalls != 1 || synthesisCalls != 1 {
		t.Fatalf("router=%d synthesis=%d", routerCalls, synthesisCalls)
	}
	session := payload["session"].(map[string]any)
	reports := session["reports"].([]any)
	if len(reports) != 4 {
		t.Fatalf("reports = %d", len(reports))
	}
	first := reports[0].(map[string]any)
	if fit := first["platformFit"].([]any); len(fit) != 2 {
		t.Fatalf("duplicate platform-fit feature must collapse to one, got %v", fit)
	}
	if questions := first["questions"].([]any); len(questions) != 1 {
		t.Fatalf("questions = %v", questions)
	}
	rec, payload = call(t, handler, http.MethodGet, "/api/builder?worker=release-clerk", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("builder: %d %s", rec.Code, rec.Body.String())
	}
	team := payload["team"].(map[string]any)
	if len(team["features"].([]any)) != len(benchFeatureCatalogue) || len(team["permanent"].([]any)) != 3 {
		t.Fatalf("team = %v", team)
	}
}

func TestPlatformFitVocabularyIsClosed(t *testing.T) {
	cases := []struct {
		report BuilderExpertReport
		want   string
	}{
		{BuilderExpertReport{Summary: "ok", PlatformFit: []PlatformFitItem{{Feature: "database", Status: "used", Note: "x"}}}, "unknown Bench feature"},
		{BuilderExpertReport{Summary: "ok", PlatformFit: []PlatformFitItem{{Feature: "state_kv", Status: "maybe", Note: "x"}}}, "unknown platform-fit status"},
		{BuilderExpertReport{Summary: "ok", Questions: []string{"a", "b", "c", "d", "e"}}, "more than four questions"},
	}
	for _, tc := range cases {
		if _, err := normalizeBuilderReport(tc.report); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("report %+v: err = %v, want %q", tc.report, err, tc.want)
		}
	}
	ok, err := normalizeBuilderReport(BuilderExpertReport{Summary: "ok", PlatformFit: []PlatformFitItem{{Feature: "heartbeat", Status: "not_needed", Note: " no watch work "}}})
	if err != nil || len(ok.PlatformFit) != 1 || ok.PlatformFit[0].Note != "no watch work" {
		t.Fatalf("valid report rejected: %+v %v", ok, err)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(builderExpertSchema()), &schema); err != nil {
		t.Fatalf("expert schema is not JSON: %v", err)
	}
	text := builderExpertSchema()
	for _, id := range benchFeatureIDs {
		if !strings.Contains(text, `"`+id+`"`) {
			t.Fatalf("expert schema does not offer feature %q", id)
		}
	}
	for _, expected := range []string{`"platformFit"`, `"questions"`, `"not_needed"`, `"misused"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expert schema missing %q", expected)
		}
	}
}

func TestPlatformDossierForNewWorkerHasNoHome(t *testing.T) {
	app, _, _ := newTestApp(t)
	dossier := app.platformDossier(context.Background(), Worker{}, AgentDefinition{Checks: []WorkerCheck{{Kind: "file_nonempty", Path: "notes/latest.md"}}})
	if dossier.Home != nil {
		t.Fatalf("a new worker has no live home, got %+v", dossier.Home)
	}
	if !strings.Contains(dossier.CheckTemplate, "notes/latest.md") || dossier.Suite["agent"] != "agent fake" || len(dossier.Features) != len(benchFeatureCatalogue) {
		t.Fatalf("dossier = %+v", dossier)
	}
	if !strings.Contains(dossier.AgentContract, "65536") || !strings.Contains(dossier.HireContract, "-checkpoint <request-id>") {
		t.Fatalf("contracts lost their exact numbers or argv: %q %q", dossier.AgentContract, dossier.HireContract)
	}
}

// waitForBuilderTurn polls GET /api/builder the way the page does until the
// latest turn has finished, and returns that payload.
func waitForBuilderTurn(t *testing.T, handler http.Handler, scope string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		rec, payload := call(t, handler, http.MethodGet, "/api/builder?worker="+scope, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("builder poll: %d %s", rec.Code, rec.Body.String())
		}
		if session, ok := payload["session"].(map[string]any); ok {
			if turns, ok := session["turns"].([]any); ok && len(turns) > 0 {
				status := turns[len(turns)-1].(map[string]any)["status"]
				if status == "complete" || status == "failed" {
					return payload
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("builder turn did not finish: %v", payload)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func latestBuilderTurn(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	turns := payload["session"].(map[string]any)["turns"].([]any)
	return turns[len(turns)-1].(map[string]any)
}

func TestAdvisoryExpertFailureIsToleratedAndRequiredFailureNamesTheCulprit(t *testing.T) {
	app, _, _ := newTestApp(t)
	handler := app.routes()
	if err := app.store.SaveModelProof(ModelProof{Model: "openai/fake-model", OK: true, Output: "ok", At: app.now()}); err != nil {
		t.Fatal(err)
	}
	replies := t.TempDir()
	t.Setenv("FAKE_BUILDER_ROUTER_REPLY", writeTestReply(t, replies, "router.json", `{"experts":[{"name":"Release engineering lead","focus":"Review what shipped.","reason":"Release role."},{"name":"Technical editor","focus":"Review structure.","reason":"Published prose."}]}`))
	t.Setenv("FAKE_BUILDER_EXPERT_REPLY", writeTestReply(t, replies, "expert.json", `{"summary":"Sound.","recommendations":[],"risks":[],"questions":[],"platformFit":[]}`))
	// The second task expert leaves everything blank, as a real model did.
	t.Setenv("FAKE_BUILDER_EXPERT_REPLY_05", writeTestReply(t, replies, "blank.json", `{"summary":"","recommendations":[],"risks":[],"questions":[],"platformFit":[]}`))
	t.Setenv("FAKE_BUILDER_SYNTHESIS_REPLY", writeTestReply(t, replies, "synthesis.json", `{"message":"Ready.","ready":true,"changes":[],"definition":{"name":"Notes","purpose":"Write notes.","network":false,"files":{"goal":"# Outcome\nNotes.\n","agents":"# Method\nWrite work/requests/{request_id}/RESULT.md.\n","soul":"","plan":"","memory":"","heartbeat":""},"checks":[]}}`))

	rec, _ := call(t, handler, http.MethodPost, "/api/builder/chat", map[string]string{"message": "Build a notes worker."})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
	}
	payload := waitForBuilderTurn(t, handler, "")
	turn := latestBuilderTurn(t, payload)
	if turn["status"] != "complete" {
		t.Fatalf("an advisory failure must not sink the turn: %v", turn)
	}
	if reports := payload["session"].(map[string]any)["reports"].([]any); len(reports) != 4 {
		t.Fatalf("expected the four sound reviews, got %d", len(reports))
	}
	failures := turn["failures"].([]any)
	if len(failures) != 1 || !strings.Contains(failures[0].(string), "Technical editor (advisory)") || !strings.Contains(failures[0].(string), "continued without this review") {
		t.Fatalf("advisory failure not recorded: %v", failures)
	}
	if states := turn["reviewStates"].(map[string]any); states["domain-2"] != "failed" || states["bench-platform"] != "done" || states["domain-1"] != "done" {
		t.Fatalf("live states after an advisory failure = %v", states)
	}

	// A required reviewer fails while the platform architect is still running:
	// the culprit is named, the architect is stopped with its process tree, and
	// the turn fails promptly.
	t.Setenv("FAKE_BUILDER_EXPERT_SLEEP_01", "4")
	t.Setenv("FAKE_BUILDER_EXPERT_REPLY_02", writeTestReply(t, replies, "broken.json", `not json at all`))
	started := time.Now()
	rec, _ = call(t, handler, http.MethodPost, "/api/builder/chat", map[string]string{"message": "Add citations."})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("second chat: %d %s", rec.Code, rec.Body.String())
	}
	payload = waitForBuilderTurn(t, handler, "")
	elapsed := time.Since(started)
	turn = latestBuilderTurn(t, payload)
	errText := turn["error"].(string)
	if turn["status"] != "failed" || !strings.Contains(errText, "Evidence and acceptance architect") || strings.Contains(errText, "Bench platform architect") {
		t.Fatalf("the culprit must be named, got %v", turn)
	}
	joined := fmt.Sprint(turn["failures"])
	if !strings.Contains(joined, "Bench platform architect: stopped after a required review failed") {
		t.Fatalf("the cancelled reviewer must be recorded as a victim: %v", turn["failures"])
	}
	if states := turn["reviewStates"].(map[string]any); states["evidence"] != "failed" || states["bench-platform"] != "stopped" {
		t.Fatalf("live states after a required failure = %v", states)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("cancelling did not stop the sleeping reviewer's process tree: took %s", elapsed)
	}
	if session := payload["session"].(map[string]any); session["ready"] != true {
		t.Fatalf("a failed turn must keep the last good proposal: %v", session["ready"])
	}
}

func TestBuilderTurnRunsInBackgroundAndGuardsConcurrentChanges(t *testing.T) {
	app, _, _ := newTestApp(t)
	handler := app.routes()
	if err := app.store.SaveModelProof(ModelProof{Model: "openai/fake-model", OK: true, Output: "ok", At: app.now()}); err != nil {
		t.Fatal(err)
	}
	replies := t.TempDir()
	t.Setenv("FAKE_BUILDER_ROUTER_REPLY", writeTestReply(t, replies, "router.json", `{"experts":[{"name":"Editor","focus":"Structure.","reason":"Prose."}]}`))
	t.Setenv("FAKE_BUILDER_EXPERT_REPLY", writeTestReply(t, replies, "expert.json", `{"summary":"Fine.","recommendations":[],"risks":[]}`))
	t.Setenv("FAKE_BUILDER_SYNTHESIS_REPLY", writeTestReply(t, replies, "synthesis.json", `{"message":"Ready.","ready":true,"changes":[],"definition":{"name":"Notes","purpose":"Write notes.","network":false,"files":{"goal":"# Outcome\nNotes.\n","agents":"# Method\nWrite work/requests/{request_id}/RESULT.md.\n","soul":"","plan":"","memory":"","heartbeat":""},"checks":[]}}`))
	t.Setenv("FAKE_ASK_SLEEP", "1")

	rec, payload := call(t, handler, http.MethodPost, "/api/builder/chat", map[string]string{"message": "Build a notes worker."})
	if rec.Code != http.StatusAccepted || latestBuilderTurn(t, payload)["status"] != "routing" {
		t.Fatalf("chat must be accepted at once with a routing turn: %d %v", rec.Code, payload)
	}
	for _, attempt := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/builder/apply", map[string]string{"workerSlug": ""}},
		{http.MethodDelete, "/api/builder", nil},
		{http.MethodPost, "/api/builder/chat", map[string]string{"message": "Another message."}},
	} {
		rec, payload = call(t, handler, attempt.method, attempt.path, attempt.body)
		if rec.Code != http.StatusConflict || payload["error"].(map[string]any)["code"] != "builder-busy" {
			t.Fatalf("%s %s during a turn = %d %s", attempt.method, attempt.path, rec.Code, rec.Body.String())
		}
	}
	rec, payload = call(t, handler, http.MethodGet, "/api/builder", nil)
	if rec.Code != http.StatusOK || payload["active"] != true || latestBuilderTurn(t, payload)["status"] != "routing" {
		t.Fatalf("progress must be visible while the turn runs: %d %v", rec.Code, payload)
	}
	payload = waitForBuilderTurn(t, handler, "")
	if latestBuilderTurn(t, payload)["status"] != "complete" || payload["active"] != false {
		t.Fatalf("turn should complete in the background: %v", payload)
	}
	turn := latestBuilderTurn(t, payload)
	states := turn["reviewStates"].(map[string]any)
	if len(states) != 4 {
		t.Fatalf("every reviewer needs a live state: %v", states)
	}
	for id, st := range states {
		if st != "done" {
			t.Fatalf("reviewer %s ended in state %v", id, st)
		}
	}
	if turn["routedAt"] == nil || turn["reviewedAt"] == nil || turn["completedAt"] == nil {
		t.Fatalf("stage timestamps missing: %v", turn)
	}
	rec, _ = call(t, handler, http.MethodPost, "/api/builder/apply", map[string]string{"workerSlug": ""})
	if rec.Code != http.StatusOK {
		t.Fatalf("apply after the turn: %d %s", rec.Code, rec.Body.String())
	}
}

func TestOrphanedBuilderTurnIsMarkedFailedOnRead(t *testing.T) {
	app, _, _ := newTestApp(t)
	handler := app.routes()
	session := BuilderSession{ID: "builder-20260903-090000-abc123", Model: "openai/fake-model", Messages: []BuilderMessage{}, Turns: []BuilderTurn{{Number: 1, Status: "reviewing", Message: "Build it.", StartedAt: app.now()}}, CreatedAt: app.now(), UpdatedAt: app.now()}
	if err := app.store.SaveBuilderSession(session); err != nil {
		t.Fatal(err)
	}
	rec, payload := call(t, handler, http.MethodGet, "/api/builder", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("builder: %d %s", rec.Code, rec.Body.String())
	}
	turn := latestBuilderTurn(t, payload)
	if turn["status"] != "failed" || !strings.Contains(turn["error"].(string), "Hire stopped while this turn was running") {
		t.Fatalf("orphaned turn must be closed honestly: %v", turn)
	}
}

func TestEmptySummaryFallsBackToFirstRecommendation(t *testing.T) {
	report, err := normalizeBuilderReport(BuilderExpertReport{Summary: "  ", Recommendations: []string{"Name the source of truth."}})
	if err != nil || report.Summary != "Name the source of truth." {
		t.Fatalf("summary fallback = %q, %v", report.Summary, err)
	}
	if _, err := normalizeBuilderReport(BuilderExpertReport{}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("a review with nothing in it must be rejected, got %v", err)
	}
}

func TestRunCommandStopsTheWholeProcessTree(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "tree", "#!/bin/sh\nsleep 17.31 &\nsleep 17.31\n")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	_, _, code, err := runCommand(ctx, script, nil, dir, nil, nil, time.Minute)
	if time.Since(started) > 2*time.Second {
		t.Fatalf("cancelled command did not return promptly: %s", time.Since(started))
	}
	if code != -1 || err == nil || !strings.Contains(err.Error(), "stopped before it finished") {
		t.Fatalf("code=%d err=%v", code, err)
	}
	time.Sleep(150 * time.Millisecond)
	if out, _ := exec.Command("pgrep", "-f", "sleep 17.31").Output(); len(strings.TrimSpace(string(out))) > 0 {
		t.Fatalf("a grandchild survived the cancel: pids %s", out)
	}
}

func TestExpertApplyCanBeRevertedUntilSomethingElseChanges(t *testing.T) {
	app, _, _ := newTestApp(t)
	handler := app.routes()
	rec, _ := call(t, handler, http.MethodPost, "/api/workers", createWorkerRequest{Name: "Release Clerk", Purpose: "Old purpose."})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if err := app.store.SaveModelProof(ModelProof{Model: "openai/fake-model", OK: true, Output: "ok", At: app.now()}); err != nil {
		t.Fatal(err)
	}
	replies := t.TempDir()
	t.Setenv("FAKE_BUILDER_ROUTER_REPLY", writeTestReply(t, replies, "router.json", `{"experts":[{"name":"Release manager","focus":"Review the release workflow.","reason":"This is a release role."}]}`))
	t.Setenv("FAKE_BUILDER_EXPERT_REPLY", writeTestReply(t, replies, "expert.json", `{"summary":"Bounded.","recommendations":[],"risks":[]}`))
	t.Setenv("FAKE_BUILDER_SYNTHESIS_REPLY", writeTestReply(t, replies, "update.json", `{"message":"Ready.","ready":true,"changes":["Renamed"],"definition":{"name":"Release Curator","purpose":"Curate notes.","network":true,"files":{"goal":"# Outcome\nCurate the note.\n","agents":"# Method\nWrite work/requests/{request_id}/RESULT.md.\n","soul":"","plan":"","memory":"","heartbeat":""},"checks":[{"kind":"file_nonempty","path":"vendor/three.min.js","description":"A person-supplied library","text":"","minimumBytes":0}]}}`))
	applyOnce := func() {
		rec, _ := call(t, handler, http.MethodPost, "/api/builder/chat", map[string]string{"workerSlug": "release-clerk", "message": "Refine it."})
		if rec.Code != http.StatusAccepted {
			t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
		}
		waitForBuilderTurn(t, handler, "release-clerk")
		rec, _ = call(t, handler, http.MethodPost, "/api/builder/apply", map[string]string{"workerSlug": "release-clerk"})
		if rec.Code != http.StatusOK {
			t.Fatalf("apply: %d %s", rec.Code, rec.Body.String())
		}
	}
	applyOnce()
	rec, payload := call(t, handler, http.MethodGet, "/api/builder?worker=release-clerk", nil)
	revertable, _ := payload["revertable"].(map[string]any)
	if rec.Code != http.StatusOK || revertable == nil || revertable["previousName"] != "Release Clerk" || revertable["appliedName"] != "Release Curator" {
		t.Fatalf("apply must be revertable: %d %v", rec.Code, payload)
	}
	rec, payload = call(t, handler, http.MethodPost, "/api/builder/revert", map[string]string{"workerSlug": "release-clerk"})
	if rec.Code != http.StatusOK {
		t.Fatalf("revert: %d %s", rec.Code, rec.Body.String())
	}
	worker := payload["worker"].(map[string]any)
	if worker["name"] != "Release Clerk" || worker["network"] != false || worker["checkState"] != "valid" {
		t.Fatalf("reverted worker = %v", worker)
	}
	goal, err := os.ReadFile(filepath.Join(app.homeDir("release-clerk"), "GOAL.md"))
	if err != nil || !strings.Contains(string(goal), "Old purpose.") {
		t.Fatalf("GOAL.md not restored: %q %v", goal, err)
	}
	if checks, err := readWorkerChecks(app.homeDir("release-clerk")); err != nil || len(checks) != 0 {
		t.Fatalf("checks not restored: %+v %v", checks, err)
	}
	rec, payload = call(t, handler, http.MethodGet, "/api/builder?worker=release-clerk", nil)
	if rec.Code != http.StatusOK || payload["revertable"] != nil {
		t.Fatalf("a reverted apply must not be offered again: %v", payload["revertable"])
	}
	if rec, _ = call(t, handler, http.MethodPost, "/api/builder/revert", map[string]string{"workerSlug": "release-clerk"}); rec.Code != http.StatusConflict {
		t.Fatalf("second revert should be refused, got %d", rec.Code)
	}

	// A hand edit after an apply protects itself: the revert is withdrawn.
	applyOnce()
	rec, _ = call(t, handler, http.MethodPut, "/api/workers/release-clerk/definition", map[string]string{"name": "GOAL.md", "content": "# Outcome\nEdited by hand.\n"})
	if rec.Code != http.StatusOK {
		t.Fatalf("manual edit: %d %s", rec.Code, rec.Body.String())
	}
	rec, payload = call(t, handler, http.MethodGet, "/api/builder?worker=release-clerk", nil)
	if rec.Code != http.StatusOK || payload["revertable"] != nil {
		t.Fatalf("revert must be withdrawn after a manual edit: %v", payload["revertable"])
	}
	if rec, _ = call(t, handler, http.MethodPost, "/api/builder/revert", map[string]string{"workerSlug": "release-clerk"}); rec.Code != http.StatusConflict {
		t.Fatalf("revert after a manual edit should be refused, got %d", rec.Code)
	}
}

func TestModelProofsArePerModel(t *testing.T) {
	app, _, _ := newTestApp(t)
	handler := app.routes()
	if err := app.store.SaveModelProof(ModelProof{Model: "openai/fake-model", OK: true, Output: "ok", At: app.now()}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_ASK_REPLY", writeTestReply(t, t.TempDir(), "ok.txt", "ok\n"))
	rec, payload := call(t, handler, http.MethodPost, "/api/model/prove", map[string]string{"model": "openai/second-model"})
	if rec.Code != http.StatusOK || payload["proof"].(map[string]any)["ok"] != true || payload["model"].(map[string]any)["state"] != "ready" {
		t.Fatalf("prove a named model: %d %v", rec.Code, payload)
	}
	rec, payload = call(t, handler, http.MethodGet, "/api/runtime", nil)
	def := payload["model"].(map[string]any)
	if rec.Code != http.StatusOK || def["state"] != "ready" || def["model"] != "openai/fake-model" {
		t.Fatalf("proving a second model must not un-prove the default: %v", def)
	}
	if proved := def["proved"].([]any); len(proved) != 2 {
		t.Fatalf("both proofs must be listed: %v", proved)
	}
	rec, _ = call(t, handler, http.MethodPost, "/api/workers", createWorkerRequest{Name: "Second", Purpose: "Uses the second model.", Model: "openai/second-model"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	rec, payload = call(t, handler, http.MethodGet, "/api/builder?worker=second", nil)
	if assistant := payload["assistant"].(map[string]any); rec.Code != http.StatusOK || assistant["ready"] != true {
		t.Fatalf("a worker on a proved model must be ready: %v", payload["assistant"])
	}
	if rec, _ = call(t, handler, http.MethodPost, "/api/model/prove", map[string]string{"model": "nonsense"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("an invalid model name must be refused, got %d", rec.Code)
	}
	var legacy ModelProof
	if err := readJSON(filepath.Join(app.store.root, "model-proof.json"), &legacy); err != nil || legacy.Model != "openai/second-model" {
		t.Fatalf("legacy proof file must hold the latest proof: %+v %v", legacy, err)
	}
}
