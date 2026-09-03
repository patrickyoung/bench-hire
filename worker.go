package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type homeReceipt struct {
	Valid            bool      `json:"valid"`
	Message          string    `json:"message,omitempty"`
	DefinitionSHA256 string    `json:"definitionSha256,omitempty"`
	CompiledSHA256   string    `json:"compiledSha256,omitempty"`
	CheckSHA256      string    `json:"checkSha256,omitempty"`
	Authority        string    `json:"authority,omitempty"`
	CheckedAt        time.Time `json:"checkedAt"`
}

type createWorkerRequest struct {
	Name    string        `json:"name"`
	Purpose string        `json:"purpose"`
	Model   string        `json:"model"`
	Network bool          `json:"network"`
	Routine *routineInput `json:"routine,omitempty"`
}

type routineInput struct {
	Title        string `json:"title"`
	Instructions string `json:"instructions"`
	Every        string `json:"every"`
	At           string `json:"at"`
	Weekday      int    `json:"weekday"`
	Check        string `json:"check"`
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(name string) string {
	slug := nonSlug.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	return slug
}

// checkScript is the executable definition of done written into every home.
// It lives in bin/, which Cage keeps read-only for the model, and it reads the
// request from the home root for the same reason.
const checkScript = `#!/bin/sh
# Bench Hire: accept when the current request's RESULT.md exists and the
# request's own check (if any) passes. Exit 0 accepts, 1 rejects.
home=$(cd "$(dirname "$0")/.." && pwd)
id=$(sed -n 's/^id: //p' "$home/REQUEST.md" 2>/dev/null | head -1)
[ -n "$id" ] || { echo "no current request in REQUEST.md"; exit 1; }
[ -s "$home/work/requests/$id/RESULT.md" ] || { echo "missing work/requests/$id/RESULT.md"; exit 1; }
check=$(sed -n 's/^check: //p' "$home/REQUEST.md" | head -1)
[ -z "$check" ] || (cd "$home/work" && sh -c "$check" </dev/null) || { echo "request check failed: $check"; exit 1; }
exit 0
`

func goalTemplate(purpose string) string {
	return "# Outcome\n\n" + strings.TrimSpace(purpose) + `

Each request that arrives is one unit of work. The durable end state for a
request is a complete, honest ` + "`work/requests/<id>/RESULT.md`" + ` that answers or
delivers what the request asked, plus any files it names under ` + "`work/`" + `.

## Acceptance evidence

` + "`bin/check`" + ` reads the current request from ` + "`REQUEST.md`" + ` at the home root and
accepts only when ` + "`work/requests/<id>/RESULT.md`" + ` exists and is not empty, and
the request's own check command (when it names one) exits 0 from ` + "`work/`" + `.

## Constraints and stop conditions

- Never claim a result you did not write to disk.
- Keep facts you will need next time under ` + "`state/kv/`" + `.
- Stop and report in RESULT.md when the request needs human judgment,
  credentials, or authority you do not have; say exactly what is missing.
`
}

func agentsTemplate(name, purpose string) string {
	return "# Operating instructions\n\nYou are " + strings.TrimSpace(name) + ", a digital worker. Your job: " + strings.TrimSpace(purpose) + `

## How work arrives

- The current request is in ` + "`REQUEST.md`" + ` at the home root (read-only). Its
  ` + "`id:`" + ` line names the request and ` + "`check:`" + ` names an optional command that
  must exit 0 from ` + "`work/`" + ` before the request counts as done.
- Read it first. Then read ` + "`state/plan.md`" + ` and list ` + "`state/kv/`" + ` for what you
  already know from earlier requests.

## How work is delivered

- Write the deliverable to ` + "`work/requests/<id>/RESULT.md`" + `: one summary line,
  then the result, then anything left undone and why.
- Put files the request asks for under ` + "`work/`" + ` and name them in RESULT.md.
- Keep facts worth remembering as small files under ` + "`state/kv/`" + ` (one fact per
  file; the file name is the key). Keep running notes in ` + "`state/plan.md`" + `.
- Never rewrite definition files. Propose changes under ` + "`work/proposals/`" + `.
- Never invoke an external connector directly. Write strict Action JSON under
  ` + "`work/actions/`" + ` and say so in RESULT.md; a person reviews it.
- Inspect state on demand instead of loading it wholesale.
`
}

// createWorker scaffolds through agent new, writes Hire's definition files,
// and proves the home with agent check before calling it deployed.
func (a *application) createWorker(ctx context.Context, in createWorkerRequest) (Worker, []Routine, error) {
	name := strings.TrimSpace(in.Name)
	purpose := strings.TrimSpace(in.Purpose)
	if name == "" || purpose == "" {
		return Worker{}, nil, errors.New("a worker needs a name and a job description")
	}
	if len(name) > 120 || len(purpose) > 8192 {
		return Worker{}, nil, errors.New("the name or job description is too long")
	}
	slug := slugify(name)
	if !validSlug(slug) {
		return Worker{}, nil, errors.New("the name must contain at least one letter or digit")
	}
	if _, err := a.store.Worker(slug); err == nil {
		return Worker{}, nil, fmt.Errorf("a worker named %s already exists", slug)
	}
	model := strings.TrimSpace(in.Model)
	if model == "" {
		model = a.defaultModel()
	}
	if model != "" {
		if err := validateModel(model); err != nil {
			return Worker{}, nil, err
		}
	}
	var routine *Routine
	if in.Routine != nil && strings.TrimSpace(in.Routine.Instructions) != "" {
		r, err := a.buildRoutine(slug, *in.Routine, "creation", a.now())
		if err != nil {
			return Worker{}, nil, err
		}
		routine = &r
	}
	home := a.homeDir(slug)
	if _, err := os.Lstat(home); err == nil {
		return Worker{}, nil, fmt.Errorf("a home directory already exists at %s", home)
	}
	if err := os.MkdirAll(filepath.Dir(home), 0o700); err != nil {
		return Worker{}, nil, err
	}
	if _, stderr, code, err := a.tools.run(ctx, "agent", []string{"new", home}, "", nil, 30*time.Second); err != nil || code != 0 {
		_ = os.RemoveAll(home)
		return Worker{}, nil, fmt.Errorf("agent new failed: %s", firstLine(stderr, err))
	}
	worker := Worker{Slug: slug, Name: name, Purpose: purpose, Model: model, Network: in.Network, CreatedAt: a.now()}
	if err := writeHomeFiles(home, worker); err != nil {
		_ = os.RemoveAll(home)
		return Worker{}, nil, err
	}
	receipt := a.checkHome(ctx, home)
	worker.Receipt = receipt
	if receipt.Valid {
		now := a.now()
		worker.DeployedAt = &now
		worker.Enabled = true
		worker.CheckState = "valid"
	} else {
		worker.CheckState = "invalid"
		worker.CheckMessage = receipt.Message
	}
	if err := a.store.SaveWorker(worker); err != nil {
		return Worker{}, nil, err
	}
	var routines []Routine
	if routine != nil {
		if err := a.store.SaveRoutine(*routine); err != nil {
			return Worker{}, nil, err
		}
		routines = append(routines, *routine)
	}
	return worker, routines, nil
}

func writeHomeFiles(home string, w Worker) error {
	files := map[string]string{
		"GOAL.md":   goalTemplate(w.Purpose),
		"AGENTS.md": agentsTemplate(w.Name, w.Purpose),
	}
	for name, content := range files {
		if err := writeFileAtomic(filepath.Join(home, name), []byte(content), false); err != nil {
			return err
		}
	}
	checkPath := filepath.Join(home, "bin", "check")
	if err := writeFileAtomic(checkPath, []byte(checkScript), false); err != nil {
		return err
	}
	if err := os.Chmod(checkPath, 0o755); err != nil {
		return err
	}
	for _, dir := range []string{"work/requests", "work/proposals", "work/actions", "state/kv", "tools", "skills"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// checkHome runs the model-free validation and reads the exact composition
// digests that agent show reports. It never invokes a model.
func (a *application) checkHome(ctx context.Context, home string) homeReceipt {
	receipt := homeReceipt{CheckedAt: a.now()}
	_, stderr, code, err := a.tools.run(ctx, "agent", []string{"check", home}, "", nil, 30*time.Second)
	if err != nil || code != 0 {
		receipt.Message = "agent check failed: " + firstLine(stderr, err)
		return receipt
	}
	stdout, stderr, code, err := a.tools.run(ctx, "agent", []string{"show", home}, "", nil, 30*time.Second)
	if err != nil || code != 0 {
		receipt.Message = "agent show failed: " + firstLine(stderr, err)
		return receipt
	}
	receipt.Valid = true
	receipt.Message = "agent check accepted the home"
	for _, line := range strings.Split(string(stdout), "\n") {
		key, value, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		switch key {
		case "definition-sha256":
			receipt.DefinitionSHA256 = strings.TrimSpace(value)
		case "compiled-sha256":
			receipt.CompiledSHA256 = strings.TrimSpace(value)
		case "check-sha256":
			receipt.CheckSHA256 = strings.TrimSpace(value)
		case "default-authority":
			receipt.Authority = strings.TrimSpace(value)
		}
	}
	return receipt
}

func firstLine(stderr []byte, err error) string {
	text := strings.TrimSpace(string(bytes.TrimSpace(stderr)))
	if text == "" && err != nil {
		text = err.Error()
	}
	if text == "" {
		text = "no output"
	}
	if i := strings.LastIndex(text, "\n"); i >= 0 {
		text = text[i+1:]
	}
	if len(text) > 400 {
		text = text[:400]
	}
	return text
}

func (a *application) buildRoutine(slug string, in routineInput, source string, now time.Time) (Routine, error) {
	title := strings.TrimSpace(in.Title)
	instructions := strings.TrimSpace(in.Instructions)
	if instructions == "" {
		return Routine{}, errors.New("a routine needs instructions")
	}
	if title == "" {
		title = summaryLine(instructions)
	}
	every := strings.TrimSpace(in.Every)
	if every == "" {
		every = "daily"
	}
	at := strings.TrimSpace(in.At)
	if at == "" && every != "hourly" {
		at = "09:00"
	}
	if err := validateCadence(every, at, in.Weekday); err != nil {
		return Routine{}, err
	}
	id := "routine-" + now.UTC().Format("20060102-150405") + "-" + randomHex(3)
	return Routine{
		ID:           id,
		WorkerSlug:   slug,
		Title:        title,
		Instructions: instructions,
		Check:        strings.TrimSpace(in.Check),
		Every:        every,
		At:           at,
		Weekday:      in.Weekday,
		Enabled:      true,
		NextDue:      nextDue(every, at, in.Weekday, now, a.location),
		Source:       source,
		CreatedAt:    now,
	}, nil
}

func summaryLine(text string) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(text), "\n", 2)[0])
	line = strings.TrimLeft(line, "#- ")
	if len(line) > 80 {
		cut := strings.LastIndex(line[:80], " ")
		if cut < 30 {
			cut = 80
		}
		line = strings.TrimSpace(line[:cut]) + "…"
	}
	if line == "" {
		line = "Untitled request"
	}
	return line
}

// toolset resolves the Bench executables once and runs them with literal
// argument arrays, explicit timeouts, and separate output capture.
type toolset struct {
	paths   map[string]string
	realAsk string
}

var requiredTools = []string{"agent", "tend", "ask", "ply", "brief", "cage"}

func newToolset(binDir string) *toolset {
	t := &toolset{paths: map[string]string{}}
	for _, name := range requiredTools {
		if override := os.Getenv("HIRE_" + strings.ToUpper(name)); override != "" {
			if abs, err := filepath.Abs(override); err == nil {
				t.paths[name] = abs
			}
			continue
		}
		if binDir != "" {
			candidate := filepath.Join(binDir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				t.paths[name] = candidate
				continue
			}
		}
		if path, err := exec.LookPath(name); err == nil {
			if abs, err := filepath.Abs(path); err == nil {
				t.paths[name] = abs
			}
		}
	}
	return t
}

func (t *toolset) path(name string) string { return t.paths[name] }

func (t *toolset) run(ctx context.Context, name string, args []string, dir string, stdin []byte, timeout time.Duration) ([]byte, []byte, int, error) {
	path := t.paths[name]
	if path == "" {
		return nil, nil, -1, fmt.Errorf("%s is not installed on PATH", name)
	}
	return runCommand(ctx, path, args, dir, stdin, nil, timeout)
}

func runCommand(ctx context.Context, path string, args []string, dir string, stdin []byte, env []string, timeout time.Duration) ([]byte, []byte, int, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, path, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	} else {
		cmd.Stdin = nil
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
			err = nil
		} else {
			code = -1
		}
	}
	if runCtx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("%s timed out after %s", filepath.Base(path), timeout)
	}
	return stdout.Bytes(), stderr.Bytes(), code, err
}
