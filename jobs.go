package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Job mirrors one line of tend list.
type Job struct {
	ID              string   `json:"id"`
	Status          string   `json:"status"`
	SerialKey       string   `json:"serial_key"`
	Cwd             string   `json:"cwd"`
	Argv            []string `json:"argv"`
	CheckArgv       []string `json:"check_argv,omitempty"`
	RunDir          string   `json:"run_dir"`
	CreatedUS       int64    `json:"created_us"`
	UpdatedUS       int64    `json:"updated_us"`
	NotBeforeUS     int64    `json:"not_before_us"`
	CancelRequested bool     `json:"cancel_requested"`
}

type JobEvent struct {
	Job       string         `json:"job"`
	Seq       int            `json:"seq"`
	Kind      string         `json:"kind"`
	CreatedUS int64          `json:"created_us"`
	Payload   map[string]any `json:"payload"`
}

type attemptView struct {
	Number     int       `json:"number"`
	Status     string    `json:"status"`
	Exit       *int      `json:"exit,omitempty"`
	Note       string    `json:"note,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	Stdout     string    `json:"stdout"`
	Stderr     string    `json:"stderr"`
	Truncated  bool      `json:"truncated"`
}

var errNoJob = errors.New("no such job")

// Jobs is the seam to Tend. The real adapter shells out to the tend CLI; the
// in-memory one lets tests exercise everything above it without SQLite.
type Jobs interface {
	Submit(ctx context.Context, id, cwd string, notBefore time.Time, argv, check []string) error
	List(ctx context.Context) ([]Job, error)
	Show(ctx context.Context, id string) (Job, error)
	Events(ctx context.Context, id string) ([]JobEvent, error)
	Attempts(ctx context.Context, id string) ([]attemptView, error)
	Retry(ctx context.Context, id string) error
	Resolve(ctx context.Context, id, decision string) error
	Cancel(ctx context.Context, id string) error
	Work(ctx context.Context) (int, error)
	Check(ctx context.Context) (string, error)
}

const attemptOutputLimit = 192 * 1024

// passedEnvironment names the variables Tend may hand to a run. Provider
// credentials and the Bench override variables are the whole list; nothing
// else from the controller's environment reaches a worker.
var passedEnvironment = []string{
	"ASK_MODEL", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_VERTEX_PROJECT_ID", "CLOUD_ML_REGION", "ANTHROPIC_VERTEX_BASE_URL",
	"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_CODEX_BASE_URL", "OPENAI_CODEX_ACCOUNT_ID",
	"GEMINI_API_KEY", "GEMINI_BASE_URL", "OPENROUTER_API_KEY", "OPENROUTER_BASE_URL",
	"DEEPSEEK_API_KEY", "DEEPSEEK_BASE_URL", "CEREBRAS_API_KEY", "CEREBRAS_BASE_URL",
	"HIRE_AGENT", "AGENT_PLY", "AGENT_BRIEF", "AGENT_CAGE", "AGENT_ASK", "AGENT_HONE", "AGENT_TRAIL",
}

type tendJobs struct {
	bin  string
	root string
	env  []string
	mu   sync.Mutex
	// events are immutable once an attempt finished, so cache by updated time.
	cache map[string]cachedEvents
}

type cachedEvents struct {
	updated int64
	events  []JobEvent
}

func newTendJobs(bin, root string) (*tendJobs, error) {
	if bin == "" {
		return nil, errors.New("tend is not installed on PATH")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	env := []string{"TEND_ROOT=" + root, "TEND_PASS=" + strings.Join(passedEnvironment, " "), "TEND_JOB_MAX=" + envOr("HIRE_JOB_MAX", "45m")}
	for _, name := range []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL"} {
		if value := os.Getenv(name); value != "" {
			env = append(env, name+"="+value)
		}
	}
	for _, name := range passedEnvironment {
		if value := os.Getenv(name); value != "" {
			env = append(env, name+"="+value)
		}
	}
	return &tendJobs{bin: bin, root: root, env: env, cache: map[string]cachedEvents{}}, nil
}

func (t *tendJobs) run(ctx context.Context, timeout time.Duration, args ...string) ([]byte, []byte, int, error) {
	return runCommand(ctx, t.bin, args, t.root, nil, t.env, timeout)
}

func (t *tendJobs) Submit(ctx context.Context, id, cwd string, notBefore time.Time, argv, check []string) error {
	args := []string{"submit", "-id", id, "-C", cwd}
	// Keep the definition identical even when a retried submission crosses its
	// due time. Tend compares the complete request under a stable job ID.
	if !notBefore.IsZero() {
		args = append(args, "-at", notBefore.UTC().Format(time.RFC3339Nano))
	}
	if len(check) > 0 {
		words := make([]string, len(check))
		for i, word := range check {
			words[i] = shellQuote(word)
		}
		args = append(args, "-check", "exec "+strings.Join(words, " "))
	}
	args = append(args, "--")
	args = append(args, argv...)
	_, stderr, code, err := t.run(ctx, 30*time.Second, args...)
	if err != nil || code != 0 {
		return fmt.Errorf("tend submit: %s", firstLine(stderr, err))
	}
	return nil
}

func (t *tendJobs) List(ctx context.Context) ([]Job, error) {
	stdout, stderr, code, err := t.run(ctx, 30*time.Second, "list")
	if err != nil || code != 0 {
		return nil, fmt.Errorf("tend list: %s", firstLine(stderr, err))
	}
	var jobs []Job
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var job Job
		if err := json.Unmarshal(line, &job); err != nil {
			return nil, fmt.Errorf("tend list: %w", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, scanner.Err()
}

func (t *tendJobs) Show(ctx context.Context, id string) (Job, error) {
	stdout, stderr, code, err := t.run(ctx, 30*time.Second, "show", id)
	if err != nil || code != 0 {
		if strings.Contains(string(stderr), "no job") {
			return Job{}, errNoJob
		}
		return Job{}, fmt.Errorf("tend show: %s", firstLine(stderr, err))
	}
	var job Job
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &job); err != nil {
		return Job{}, fmt.Errorf("tend show: %w", err)
	}
	return job, nil
}

func (t *tendJobs) Events(ctx context.Context, id string) ([]JobEvent, error) {
	stdout, stderr, code, err := t.run(ctx, 30*time.Second, "events", id)
	if err != nil || code != 0 {
		return nil, fmt.Errorf("tend events: %s", firstLine(stderr, err))
	}
	var events []JobEvent
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event JobEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("tend events: %w", err)
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

func (t *tendJobs) cachedEvents(ctx context.Context, job Job) ([]JobEvent, error) {
	t.mu.Lock()
	entry, ok := t.cache[job.ID]
	t.mu.Unlock()
	if ok && entry.updated == job.UpdatedUS {
		return entry.events, nil
	}
	events, err := t.Events(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.cache[job.ID] = cachedEvents{updated: job.UpdatedUS, events: events}
	t.mu.Unlock()
	return events, nil
}

func (t *tendJobs) Attempts(ctx context.Context, id string) ([]attemptView, error) {
	job, err := t.Show(ctx, id)
	if err != nil {
		return nil, err
	}
	events, err := t.cachedEvents(ctx, job)
	if err != nil {
		return nil, err
	}
	return attemptsFromEvents(events, func(number int) (string, string, bool) {
		out, truncOut := readBounded(filepath.Join(job.RunDir, "attempts", fmt.Sprintf("%03d.out", number)))
		errText, truncErr := readBounded(filepath.Join(job.RunDir, "attempts", fmt.Sprintf("%03d.err", number)))
		return out, errText, truncOut || truncErr
	}), nil
}

// attemptsFromEvents folds Tend's immutable events into one row per attempt.
func attemptsFromEvents(events []JobEvent, read func(number int) (string, string, bool)) []attemptView {
	byAttempt := map[string]*attemptView{}
	var order []string
	for _, event := range events {
		name, _ := event.Payload["attempt"].(string)
		if name == "" {
			continue
		}
		view, ok := byAttempt[name]
		if !ok {
			view = &attemptView{Status: "running"}
			byAttempt[name] = view
			order = append(order, name)
		}
		switch event.Kind {
		case "attempt.prepared":
			if n, ok := event.Payload["number"].(float64); ok {
				view.Number = int(n)
			}
		case "attempt.started":
			view.StartedAt = time.UnixMicro(event.CreatedUS)
		case "attempt.finished", "attempt.resolved":
			view.FinishedAt = time.UnixMicro(event.CreatedUS)
			if status, ok := event.Payload["status"].(string); ok {
				view.Status = status
			}
			if exit, ok := event.Payload["exit"].(float64); ok {
				code := int(exit)
				view.Exit = &code
			}
			if note, ok := event.Payload["note"].(string); ok {
				view.Note = note
			}
		case "attempt.unknown":
			view.Status = "unknown"
			if note, ok := event.Payload["note"].(string); ok {
				view.Note = note
			}
		}
	}
	var attempts []attemptView
	for _, name := range order {
		view := byAttempt[name]
		if view.Number == 0 {
			view.Number = len(attempts) + 1
		}
		if read != nil {
			view.Stdout, view.Stderr, view.Truncated = read(view.Number)
		}
		attempts = append(attempts, *view)
	}
	sort.Slice(attempts, func(i, j int) bool { return attempts[i].Number < attempts[j].Number })
	return attempts
}

// readBounded keeps the tail of a large artifact and says so, because a
// truncated typescript that looks complete is the one failure nobody sees.
func readBounded(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	if len(data) > attemptOutputLimit {
		return "[…" + fmt.Sprint(len(data)-attemptOutputLimit) + " earlier bytes omitted]\n" + string(data[len(data)-attemptOutputLimit:]), true
	}
	return string(data), false
}

func (t *tendJobs) Retry(ctx context.Context, id string) error {
	_, stderr, code, err := t.run(ctx, 30*time.Second, "retry", id)
	if err != nil || code != 0 {
		return fmt.Errorf("tend retry: %s", firstLine(stderr, err))
	}
	return nil
}

func (t *tendJobs) Resolve(ctx context.Context, id, decision string) error {
	switch decision {
	case "retry", "done", "fail":
	default:
		return errors.New("resolution must be retry, done, or fail")
	}
	_, stderr, code, err := t.run(ctx, 2*time.Minute, "resolve", id, decision)
	if err != nil || code != 0 {
		return fmt.Errorf("tend resolve: %s", firstLine(stderr, err))
	}
	return nil
}

func (t *tendJobs) Cancel(ctx context.Context, id string) error {
	_, stderr, code, err := t.run(ctx, 30*time.Second, "cancel", id)
	if err != nil || code != 0 {
		return fmt.Errorf("tend cancel: %s", firstLine(stderr, err))
	}
	return nil
}

// Work performs one durable transition. It may block for the length of one
// worker run, which is why the runner owns its own goroutines.
func (t *tendJobs) Work(ctx context.Context) (int, error) {
	maxRun, err := time.ParseDuration(envOr("HIRE_JOB_MAX", "45m"))
	if err != nil {
		maxRun = 45 * time.Minute
	}
	_, stderr, code, err := t.run(ctx, maxRun+5*time.Minute, "work")
	if err != nil {
		return 2, fmt.Errorf("tend work: %s", firstLine(stderr, err))
	}
	if code == 2 {
		return 2, fmt.Errorf("tend work: %s", firstLine(stderr, nil))
	}
	return code, nil
}

func (t *tendJobs) Check(ctx context.Context) (string, error) {
	stdout, stderr, code, err := t.run(ctx, 2*time.Minute, "check")
	if err != nil || code != 0 {
		return "", fmt.Errorf("tend check: %s", firstLine(stderr, err))
	}
	return strings.TrimSpace(string(stdout)), nil
}

// memoryJobs is the offline stand-in. Work runs the exact argv itself so the
// hire exec path and a fake agent can be exercised end to end.
type memoryJobs struct {
	mu       sync.Mutex
	jobs     map[string]*memoryJob
	order    []string
	root     string
	now      func() time.Time
	sequence int
}

type memoryJob struct {
	Job
	events   []JobEvent
	attempts []attemptView
}

func newMemoryJobs(root string, now func() time.Time) *memoryJobs {
	return &memoryJobs{jobs: map[string]*memoryJob{}, root: root, now: now}
}

func (m *memoryJobs) Submit(_ context.Context, id, cwd string, notBefore time.Time, argv, check []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.jobs[id]; ok {
		if strings.Join(existing.Argv, "\x00") != strings.Join(argv, "\x00") || existing.Cwd != cwd ||
			strings.Join(existing.CheckArgv, "\x00") != strings.Join(check, "\x00") || existing.NotBeforeUS != notBefore.UnixMicro() {
			return fmt.Errorf("tend submit: job %s already exists with different bytes", id)
		}
		return nil
	}
	now := m.now()
	m.sequence++
	job := &memoryJob{Job: Job{ID: id, Status: "ready", SerialKey: cwd, Cwd: cwd, Argv: append([]string(nil), argv...), CheckArgv: append([]string(nil), check...), RunDir: filepath.Join(m.root, "jobs", id), CreatedUS: now.UnixMicro(), UpdatedUS: now.UnixMicro(), NotBeforeUS: notBefore.UnixMicro()}}
	job.events = append(job.events, JobEvent{Job: id, Seq: 1, Kind: "job.submitted", CreatedUS: now.UnixMicro(), Payload: map[string]any{"argv": argv, "cwd": cwd}})
	m.jobs[id] = job
	m.order = append(m.order, id)
	return nil
}

func (m *memoryJobs) List(context.Context) ([]Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var jobs []Job
	for _, id := range m.order {
		jobs = append(jobs, m.jobs[id].Job)
	}
	return jobs, nil
}

func (m *memoryJobs) Show(_ context.Context, id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return Job{}, errNoJob
	}
	return job.Job, nil
}

func (m *memoryJobs) Events(_ context.Context, id string) ([]JobEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return nil, errNoJob
	}
	return append([]JobEvent(nil), job.events...), nil
}

func (m *memoryJobs) Attempts(_ context.Context, id string) ([]attemptView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return nil, errNoJob
	}
	return append([]attemptView(nil), job.attempts...), nil
}

func (m *memoryJobs) setStatus(job *memoryJob, status string) {
	job.Status = status
	job.UpdatedUS = m.now().UnixMicro()
}

func (m *memoryJobs) Retry(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return errNoJob
	}
	if job.Status != "failed" {
		return fmt.Errorf("tend retry: job %s is %s", id, job.Status)
	}
	m.setStatus(job, "ready")
	return nil
}

func (m *memoryJobs) Resolve(ctx context.Context, id, decision string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return errNoJob
	}
	if job.Status != "unknown" {
		return fmt.Errorf("tend resolve: job %s is %s", id, job.Status)
	}
	switch decision {
	case "retry":
		m.setStatus(job, "ready")
	case "done":
		if len(job.CheckArgv) == 0 {
			return errors.New("job has no submitted -check")
		}
		// This adapter is for offline tests, but it must enforce the same
		// completion condition as Tend instead of trusting a button.
		check := append([]string(nil), job.CheckArgv...)
		cwd := job.Cwd
		m.mu.Unlock()
		out, stderr, code, runErr := runCommand(ctx, check[0], check[1:], cwd, nil, nil, 30*time.Second)
		m.mu.Lock()
		if job.Status != "unknown" {
			return errors.New("job changed while its completion check ran")
		}
		if runErr != nil || code != 0 {
			return fmt.Errorf("completion check did not pass: %s", firstLine(append(out, stderr...), runErr))
		}
		m.setStatus(job, "done")
	case "fail":
		m.setStatus(job, "failed")
	default:
		return errors.New("resolution must be retry, done, or fail")
	}
	return nil
}

func (m *memoryJobs) Cancel(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return errNoJob
	}
	if job.Status != "ready" {
		return fmt.Errorf("tend cancel: job %s is %s", id, job.Status)
	}
	m.setStatus(job, "cancelled")
	return nil
}

// MarkUnknown is a test hook standing in for a crash after start.
func (m *memoryJobs) MarkUnknown(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job, ok := m.jobs[id]; ok {
		m.setStatus(job, "unknown")
	}
}

func (m *memoryJobs) Work(ctx context.Context) (int, error) {
	m.mu.Lock()
	var picked *memoryJob
	now := m.now()
	busy := map[string]bool{}
	for _, id := range m.order {
		job := m.jobs[id]
		if job.Status == "running" || job.Status == "unknown" {
			busy[job.SerialKey] = true
		}
	}
	for _, id := range m.order {
		job := m.jobs[id]
		if job.Status == "ready" && job.NotBeforeUS <= now.UnixMicro() && !busy[job.SerialKey] {
			picked = job
			break
		}
	}
	if picked == nil {
		m.mu.Unlock()
		return 1, nil
	}
	m.setStatus(picked, "running")
	number := len(picked.attempts) + 1
	attemptName := fmt.Sprintf("attempt-%03d", number)
	picked.events = append(picked.events, JobEvent{Job: picked.ID, Seq: len(picked.events) + 1, Kind: "attempt.prepared", CreatedUS: now.UnixMicro(), Payload: map[string]any{"attempt": attemptName, "number": float64(number)}})
	picked.events = append(picked.events, JobEvent{Job: picked.ID, Seq: len(picked.events) + 1, Kind: "attempt.started", CreatedUS: now.UnixMicro(), Payload: map[string]any{"attempt": attemptName}})
	argv := append([]string(nil), picked.Argv...)
	cwd := picked.Cwd
	m.mu.Unlock()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = 125
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	finished := m.now()
	status := "done"
	if code != 0 {
		status = "failed"
	}
	if code == 125 || code < 0 || ctx.Err() != nil {
		status = "unknown"
	}
	m.setStatus(picked, status)
	exit := code
	picked.attempts = append(picked.attempts, attemptView{Number: number, Status: status, Exit: &exit, StartedAt: now, FinishedAt: finished, Stdout: stdout.String(), Stderr: stderr.String()})
	picked.events = append(picked.events, JobEvent{Job: picked.ID, Seq: len(picked.events) + 1, Kind: "attempt.finished", CreatedUS: finished.UnixMicro(), Payload: map[string]any{"attempt": attemptName, "status": status, "exit": float64(code)}})
	return 0, nil
}

func (m *memoryJobs) Check(context.Context) (string, error) { return "in-memory job store", nil }
