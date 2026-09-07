package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Keep the controller's retirement stop distinct from Ply's exit 3, which
// means a person declined an approval after a run had already begun.
const execRetiredExit = 20

// runVerify is Tend's operator-owned completion check. It never rewrites
// REQUEST.md, starts Agent, or asks a model. Another request's result cannot
// resolve this attempt, even if somebody used the home from the CLI.
func runVerify(args []string, stdout, stderr *os.File) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: hire verify HOME REQUEST.json")
		return 2
	}
	home, err := filepath.Abs(args[0])
	if err != nil {
		fmt.Fprintln(stderr, "hire verify:", err)
		return 2
	}
	var request Request
	if err := readJSON(args[1], &request); err != nil || !validID(request.ID) {
		fmt.Fprintln(stderr, "hire verify: cannot read a valid request:", err)
		return 2
	}
	workerDir := filepath.Dir(filepath.Dir(args[1]))
	lock, err := lockExecution(workerDir, false)
	if err != nil {
		fmt.Fprintln(stderr, "hire verify:", err)
		return 2
	}
	defer lock.Close()
	if err := recoverSkillTransaction(workerDir, home); err != nil {
		fmt.Fprintln(stderr, "hire verify: skill update needs attention:", err)
		return 2
	}
	home, err = requestHome(home, request)
	if err != nil {
		fmt.Fprintln(stderr, "hire verify:", err)
		return 2
	}
	records, err := readExecutionEvidence(workerDir, request.ID)
	if err != nil || len(records) == 0 {
		fmt.Fprintln(stderr, "hire verify: no recorded run definition; inspect the outcome before resolving this task")
		return 2
	}
	current, err := os.ReadFile(filepath.Join(home, "REQUEST.md"))
	if err != nil || !bytes.Equal(current, []byte(renderRequestFile(request))) {
		fmt.Fprintln(stderr, "hire verify: the home's current request does not match this task; inspect it before continuing")
		return 1
	}
	check := filepath.Join(home, "bin", "check")
	info, err := os.Lstat(check)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		fmt.Fprintln(stderr, "hire verify: bin/check must be a regular executable")
		return 2
	}
	checkBytes, err := os.ReadFile(check)
	if err != nil || contentSHA256(checkBytes) != records[0].CheckSHA256 {
		fmt.Fprintln(stderr, "hire verify: the completion check changed after this run; restore its original check before checking the result")
		return 2
	}
	cmd := exec.Command(check)
	cmd.Dir = filepath.Join(home, "work")
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return 1
		}
		fmt.Fprintln(stderr, "hire verify: completion check is broken:", err)
		return 2
	}
	return 0
}

// runExec is what Tend runs: `hire exec HOME REQUEST.json`. It runs inside
// Tend's scrubbed environment, so it takes everything from argv and files,
// writes the request where the check can read it, and hands the run to agent.
func runExec(args []string, stdout, stderr *os.File) (code int) {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: hire exec HOME REQUEST.json")
		return 2
	}
	home, err := filepath.Abs(args[0])
	if err != nil {
		fmt.Fprintln(stderr, "hire exec:", err)
		return 2
	}
	var request Request
	if err := readJSON(args[1], &request); err != nil {
		fmt.Fprintln(stderr, "hire exec:", err)
		return 2
	}
	if !validID(request.ID) {
		fmt.Fprintln(stderr, "hire exec: request has an invalid id")
		return 2
	}
	// A late claim during retirement cannot start model work. The controller
	// keeps this record outside the worker's writable roots.
	workerDir := filepath.Dir(filepath.Dir(args[1]))
	lock, err := lockExecution(workerDir, true)
	if err != nil {
		fmt.Fprintln(stderr, "hire exec:", err)
		return 125
	}
	defer lock.Close()
	if err := recoverSkillTransaction(workerDir, home); err != nil {
		fmt.Fprintln(stderr, "hire exec: skill update needs attention:", err)
		return 2
	}
	var worker Worker
	workerPath := filepath.Join(workerDir, "worker.json")
	if err := readJSON(workerPath, &worker); err != nil {
		fmt.Fprintln(stderr, "hire exec: cannot read worker status:", err)
		return 2
	}
	if worker.RetiringAt != nil || worker.RetiredAt != nil {
		fmt.Fprintln(stderr, "hire exec: worker is retiring; this task did not start")
		return execRetiredExit
	}
	parentHome := home
	home, err = requestHome(parentHome, request)
	if err != nil {
		fmt.Fprintln(stderr, "hire exec:", err)
		return 2
	}
	if request.Specialist != "" {
		worker.Name, worker.Purpose, worker.Network = request.Specialist, "", request.Network
	}
	if err := verifyUploadRefs(home, request.Uploads); err != nil {
		fmt.Fprintln(stderr, "hire exec:", err)
		return 2
	}
	evidence, err := captureExecutionEvidence(home, worker, request)
	if err != nil {
		fmt.Fprintln(stderr, "hire exec: cannot record the run definition:", err)
		return 2
	}
	evidencePath := filepath.Join(executionEvidenceDir(workerDir, request.ID), evidence.ID+".json")
	if err := writeJSONAtomic(evidencePath, evidence, true); err != nil {
		fmt.Fprintln(stderr, "hire exec: cannot save the run definition:", err)
		return 2
	}
	defer func() {
		now := time.Now().UTC()
		evidence.FinishedAt, evidence.Exit = &now, &code
		current, err := captureExecutionEvidence(home, worker, request)
		if err != nil {
			evidence.Error = "Could not compare the definition after the run: " + err.Error()
		} else {
			stable := current.DefinitionSHA256 == evidence.DefinitionSHA256 && current.CheckSHA256 == evidence.CheckSHA256
			evidence.DefinitionStable = &stable
		}
		if err := writeJSONAtomic(evidencePath, evidence, false); err != nil {
			fmt.Fprintln(stderr, "hire exec: could not finish recording run evidence:", err)
		}
	}()
	if err := writeFileAtomic(filepath.Join(home, "REQUEST.md"), []byte(renderRequestFile(request)), false); err != nil {
		fmt.Fprintln(stderr, "hire exec:", err)
		return 2
	}
	if err := os.MkdirAll(filepath.Join(home, "work", "requests", request.ID), 0o755); err != nil {
		fmt.Fprintln(stderr, "hire exec:", err)
		return 2
	}
	agentPath := os.Getenv("HIRE_AGENT")
	if agentPath == "" {
		agentPath, err = exec.LookPath("agent")
		if err != nil {
			fmt.Fprintln(stderr, "hire exec: agent is not on PATH and HIRE_AGENT is unset")
			return 2
		}
	}
	argv := agentArgs(request, parentHome)
	fmt.Fprintf(stderr, "hire: %s %s\n", filepath.Base(agentPath), strings.Join(argv, " "))
	cmd := exec.Command(agentPath, argv...)
	cmd.Dir = home
	cmd.Stdin = nil
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(stderr, "hire exec:", err)
		return 125
	}
	return 0
}

// agentArgs is the literal argument array for one request run. The
// checkpoint name is the request id, so a retry continues the same
// conversation instead of starting over.
func agentArgs(request Request, home string) []string {
	argv := []string{"run"}
	if request.Specialist != "" {
		argv = []string{"specialist", home, request.Specialist}
	}
	if request.Network {
		argv = append(argv, "-net")
	}
	if request.Model != "" {
		argv = append(argv, "-m", request.Model)
	}
	argv = append(argv, "-checkpoint", request.ID)
	if request.Specialist == "" {
		argv = append(argv, home)
	}
	argv = append(argv, "--", focusText(request))
	return argv
}
