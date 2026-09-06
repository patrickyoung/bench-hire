package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Tend continues to group work by the parent home. The target home owns the
// request, checkpoint, files and check; the parent lifecycle and execution lock
// cover its direct specialists too. No second scheduler or conversation store.
func requestHome(parent string, request Request) (string, error) {
	if request.Specialist == "" {
		return parent, nil
	}
	if !validCapabilityName(request.Specialist) {
		return "", errors.New("invalid specialist name")
	}
	home, err := withinHome(parent, "agents/"+request.Specialist)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(home)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("specialist home %s is missing or is not a directory", request.Specialist)
	}
	return home, nil
}

func requestResultPath(request Request) string {
	path := "work/requests/" + request.ID + "/RESULT.md"
	if request.Specialist != "" {
		path = "agents/" + request.Specialist + "/" + path
	}
	return path
}

func requestResultDigest(parent string, request Request) (string, error) {
	home, err := requestHome(parent, request)
	if err != nil {
		return "", err
	}
	return resultDigest(home, request.ID)
}

func (a *application) handleCreateSpecialist(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string `json:"name"`
		Purpose string `json:"purpose"`
	}
	if err := decodeJSON(r, &in, 12*1024); err != nil {
		writeError(w, http.StatusBadRequest, "json", err.Error(), "")
		return
	}
	in.Name, in.Purpose = strings.TrimSpace(in.Name), strings.TrimSpace(in.Purpose)
	if !validCapabilityName(in.Name) || in.Purpose == "" || len(in.Purpose) > 8192 {
		writeError(w, http.StatusBadRequest, "specialist", "Give the specialist a lowercase name and a standing job description of at most 8192 characters.", "")
		return
	}
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	worker, ok := a.loadMutableWorker(w, r)
	if !ok {
		return
	}
	lock, err := lockExecution(a.store.workerDir(worker.Slug), false)
	if err != nil {
		writeError(w, http.StatusConflict, "busy", err.Error(), "")
		return
	}
	defer lock.Close()
	parent := a.homeDir(worker.Slug)
	home, err := withinHome(parent, "agents/"+in.Name)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(home), 0o755)
	}
	if err == nil {
		err = os.Mkdir(home, 0o755)
	}
	if err != nil {
		writeError(w, http.StatusConflict, "specialist", err.Error(), "Choose an unused name. Existing specialist homes are never replaced.")
		return
	}
	// We reserved this new empty directory. A failed scaffold cannot damage an
	// existing home; rollback also prevents a partial child breaking the parent.
	installed := false
	defer func() {
		if !installed {
			_ = os.RemoveAll(home)
		}
	}()
	if _, stderr, code, err := a.tools.run(r.Context(), "agent", []string{"new", home}, "", nil, 30*time.Second); err != nil || code != 0 {
		writeError(w, http.StatusConflict, "specialist", "Agent could not create the specialist: "+firstLine(stderr, err), "")
		return
	}
	if err := writeHomeFiles(home, Worker{Name: in.Name, Purpose: in.Purpose}); err != nil {
		writeError(w, http.StatusInternalServerError, "specialist", err.Error(), "")
		return
	}
	receipt := a.checkHome(r.Context(), parent)
	if !receipt.Valid {
		writeError(w, http.StatusConflict, "specialist", receipt.Message, "Your specialist was not installed. Correct the job description and try again.")
		return
	}
	worker.Receipt = receipt
	if err := a.store.SaveWorker(worker); err != nil {
		writeError(w, http.StatusInternalServerError, "specialist", err.Error(), "")
		return
	}
	installed = true
	writeJSON(w, http.StatusCreated, map[string]any{"name": in.Name, "path": "agents/" + in.Name, "receipt": receipt})
}
