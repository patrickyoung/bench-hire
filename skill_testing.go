package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The check output is untrusted and may be endless. Keep a bounded prefix while
// draining both streams; cancellation kills the whole command process group.
type skillOutput struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (o *skillOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	left := (64 << 10) - len(o.data)
	if len(p) > left {
		o.truncated = true
	}
	o.data = append(o.data, p[:min(len(p), left)]...)
	return len(p), nil
}
func (o *skillOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	value := string(o.data)
	if o.truncated {
		value += "\n[Output truncated at 64 KiB]"
	}
	return value
}

func (a *application) runSkillCheck(ctx context.Context, revision SkillImprovement, dir, version string, check SkillCheck) SkillCheckResult {
	result := SkillCheckResult{Name: check.Name, Version: version, State: "broken", Exit: -1}
	started := time.Now()
	trial, err := os.MkdirTemp(dir, "trial-")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer os.RemoveAll(trial)
	skill := filepath.Join(trial, revision.Skill)
	expected := revision.After
	if version == "before" {
		expected = revision.Before
	}
	if err := copySkillBundle(filepath.Join(dir, version), skill, expected); err != nil {
		result.Error = err.Error()
		return result
	}
	temp := filepath.Join(trial, "tmp")
	if err := os.Mkdir(temp, 0700); err != nil {
		result.Error = err.Error()
		return result
	}
	args := []string{"-w", skill, "--"}
	checks := filepath.Join(dir, "checks")
	for _, arg := range check.Argv {
		args = append(args, strings.ReplaceAll(strings.ReplaceAll(arg, "{{skill}}", skill), "{{checks}}", checks))
	}
	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, a.tools.path("cage"), args...)
	cmd.Dir = skill
	// No model credentials or controller settings are needed to check a skill.
	// Cage's mandatory temporary write root is this trial's own directory.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "TMPDIR=" + temp, "BENCH_SKILL_DIR=" + skill, "PYTHONDONTWRITEBYTECODE=1"}
	if a.tools.binDir != "" {
		cmd.Env[0] = "PATH=" + a.tools.binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 3 * time.Second
	output := &skillOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	err = cmd.Run()
	result.Output, result.DurationMS = output.String(), time.Since(started).Milliseconds()
	if runCtx.Err() != nil {
		result.State = "interrupted"
		result.Error = "The check stopped before its outcome was known. Run it again explicitly."
		return result
	}
	if err == nil {
		result.Exit, result.State = 0, "passed"
		return result
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		result.Exit = exit.ExitCode()
		if result.Exit == 1 {
			result.State = "failed"
		}
		if result.Exit == 125 {
			result.Error = "Confinement could not start, or the command exited 125. No unconfined fallback was used."
		}
	} else {
		result.Error = err.Error()
	}
	return result
}

func verifySkillProposal(dir string, revision SkillImprovement) error {
	if revision.ProposalSHA256 == "" || skillProposalSHA(revision) != revision.ProposalSHA256 {
		return errors.New("the reviewed proposal changed")
	}
	if err := verifySkillBundle(filepath.Join(dir, "before"), revision.Before); err != nil {
		return err
	}
	if err := verifySkillBundle(filepath.Join(dir, "candidate"), revision.After); err != nil {
		return err
	}
	harness, err := readSkillBundle(filepath.Join(dir, "checks"))
	if err != nil || harness.SHA256 != revision.ChecksSHA256 {
		return errors.New("the proposed checks changed")
	}
	raw, err := readRegularFileLimit(filepath.Join(dir, "evidence.jsonl"), 16<<20)
	if err != nil || contentSHA256(raw) != revision.EvidenceSHA256 {
		return errors.New("the cited evidence changed")
	}
	return verifyUploadRefs(dir, revision.Uploads)
}

func (a *application) handleTestSkillImprovement(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SHA256 string `json:"sha256"`
	}
	if err := decodeJSON(r, &in, 1024); err != nil {
		writeError(w, 400, "skill", err.Error(), "")
		return
	}
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	worker, ok := a.loadMutableWorker(w, r)
	if !ok {
		return
	}
	a.skillImprovementMu.Lock()
	defer a.skillImprovementMu.Unlock()
	revision, dir, err := a.readSkillImprovement(worker.Slug, r.PathValue("skill"), r.PathValue("id"))
	if err != nil || revision.State != "review" || in.SHA256 == "" || in.SHA256 != revision.ProposalSHA256 {
		writeError(w, 409, "skill", "Reopen a ready proposal before running its checks.", "")
		return
	}
	if err := verifySkillProposal(dir, revision); err != nil {
		writeError(w, 409, "skill", err.Error(), "")
		return
	}
	revision.State, revision.Error, revision.TestedSHA256 = "testing", "", ""
	revision.Results = []SkillCheckResult{}
	if err := saveSkillImprovement(dir, revision); err != nil {
		writeError(w, 500, "skill", err.Error(), "")
		return
	}
	if a.skillImprovementActive == nil {
		a.skillImprovementActive = map[string]bool{}
	}
	a.skillImprovementActive[revision.ID] = true
	go a.testSkillImprovement(revision, dir)
	writeJSON(w, 202, map[string]any{"improvement": revision})
}

func (a *application) testSkillImprovement(revision SkillImprovement, dir string) {
	ctx, cancel := context.WithTimeout(a.backgroundContext(), 30*time.Minute)
	defer cancel()
	passed := true
	saveProgress := func() {
		a.skillImprovementMu.Lock()
		defer a.skillImprovementMu.Unlock()
		_ = saveSkillImprovement(dir, revision)
	}
	for _, version := range []string{"before", "candidate"} {
		lint := a.lintSkillCandidate(ctx, dir, revision, version)
		revision.Results = append(revision.Results, lint)
		if version == "candidate" && lint.State != "passed" {
			passed = false
		}
		saveProgress()
		for _, check := range revision.Plan.Checks {
			// Both versions use the exact same immutable harness and argument list.
			if err := verifySkillProposal(dir, revision); err != nil {
				passed = false
				revision.Error = err.Error()
				break
			}
			result := a.runSkillCheck(ctx, revision, dir, version, check)
			revision.Results = append(revision.Results, result)
			if version == "candidate" && result.State != "passed" {
				passed = false
			}
			saveProgress()
			if ctx.Err() != nil {
				passed = false
				break
			}
		}
		if ctx.Err() != nil || revision.Error != "" {
			break
		}
	}
	revision.State = "review"
	if err := verifySkillProposal(dir, revision); err != nil {
		passed = false
		revision.Error = err.Error()
	}
	if passed && ctx.Err() == nil && len(revision.Results) == 2*(len(revision.Plan.Checks)+1) {
		revision.TestedSHA256 = revision.ProposalSHA256
	}
	if ctx.Err() != nil {
		revision.Error = "Testing was interrupted. Run the checks again explicitly."
	}
	a.skillImprovementMu.Lock()
	defer a.skillImprovementMu.Unlock()
	_ = saveSkillImprovement(dir, revision)
	delete(a.skillImprovementActive, revision.ID)
}

func skillTestsPassed(revision SkillImprovement) error {
	if revision.TestedSHA256 == "" || revision.TestedSHA256 != revision.ProposalSHA256 {
		return errors.New("run the checks on this exact proposal before applying it")
	}
	count := 0
	for _, result := range revision.Results {
		if result.Version == "candidate" {
			count++
			if result.State != "passed" || result.Exit != 0 {
				return fmt.Errorf("the proposed version has not passed %s", result.Name)
			}
		}
	}
	if count != len(revision.Plan.Checks)+1 {
		return errors.New("the proposed version has incomplete test results")
	}
	return nil
}
