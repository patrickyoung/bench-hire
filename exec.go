package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// runExec is what Tend runs: `hire exec HOME REQUEST.json`. It runs inside
// Tend's scrubbed environment, so it takes everything from argv and files,
// writes the request where the check can read it, and hands the run to agent.
func runExec(args []string, stdout, stderr *os.File) int {
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
	argv := agentArgs(request, home)
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
	if request.Network {
		argv = append(argv, "-net")
	}
	if request.Model != "" {
		argv = append(argv, "-m", request.Model)
	}
	argv = append(argv, "-checkpoint", request.ID, home, "--", focusText(request))
	return argv
}
