package main

import (
	"bytes"
	"context"
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
cat "$FAKE_ASK_REPLY"
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
