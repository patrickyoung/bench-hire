package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// ExecutionEvidence keeps the job description and check used for one run.
// It lives beside Hire's requests, outside the worker's writable folders.
// Tend still owns attempts and outcomes; Ask still owns model conversations.
type ExecutionEvidence struct {
	ID               string          `json:"id"`
	RequestID        string          `json:"requestId"`
	Definition       AgentDefinition `json:"definition"`
	DefinitionSHA256 string          `json:"definitionSha256"`
	CheckSHA256      string          `json:"checkSha256"`
	CheckScript      string          `json:"checkScript,omitempty"`
	RequestSHA256    string          `json:"requestSha256"`
	StartedAt        time.Time       `json:"startedAt"`
	FinishedAt       *time.Time      `json:"finishedAt,omitempty"`
	DefinitionStable *bool           `json:"definitionStable,omitempty"`
	Exit             *int            `json:"exit,omitempty"`
	Error            string          `json:"error,omitempty"`
}

// A kernel lock survives controller restarts and is released if a process
// dies. Hire's editors and executor share it; an ordinary CLI editor does not.
func lockExecution(workerDir string, wait bool) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(workerDir, "execution.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	mode := syscall.LOCK_EX
	if !wait {
		mode |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), mode); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.New("the worker is running or its definition is being edited; try again when that finishes")
		}
		return nil, err
	}
	return f, nil
}

func contentSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func captureExecutionEvidence(home string, worker Worker, request Request) (ExecutionEvidence, error) {
	def, err := readAgentDefinition(home, worker)
	if err != nil {
		return ExecutionEvidence{}, err
	}
	path := filepath.Join(home, "bin", "check")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return ExecutionEvidence{}, errors.New("bin/check must be a regular executable")
	}
	check, err := os.ReadFile(path)
	if err != nil {
		return ExecutionEvidence{}, err
	}
	now := time.Now().UTC()
	return ExecutionEvidence{
		ID: newRequestID("run", now), RequestID: request.ID, Definition: def,
		DefinitionSHA256: agentDefinitionSHA256(def), CheckSHA256: contentSHA256(check),
		CheckScript:   string(check),
		RequestSHA256: contentSHA256([]byte(renderRequestFile(request))), StartedAt: now,
	}, nil
}

func executionEvidenceDir(workerDir, requestID string) string {
	return filepath.Join(workerDir, "evidence", requestID)
}

func readExecutionEvidence(workerDir, requestID string) ([]ExecutionEvidence, error) {
	if !validID(requestID) {
		return nil, errNotFound
	}
	var records []ExecutionEvidence
	err := readAllJSON(executionEvidenceDir(workerDir, requestID), func(path string) error {
		var record ExecutionEvidence
		if err := readJSON(path, &record); err != nil {
			return err
		}
		if record.RequestID != requestID || !validID(record.ID) {
			return fmt.Errorf("invalid execution evidence: %s", filepath.Base(path))
		}
		records = append(records, record)
		return nil
	})
	sort.Slice(records, func(i, j int) bool { return records[i].StartedAt.After(records[j].StartedAt) })
	return records, err
}
