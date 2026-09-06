package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type workerSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
}

// The capability page is a view over ordinary home files and public Bench
// commands. Brief parses skills and Hone owns learning provenance/admission.
func (a *application) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	home := a.homeDir(worker.Slug)
	var skills []workerSkill
	var warnings []string
	env := append(os.Environ(), "BRIEF_PATH="+filepath.Join(home, "skills"))
	stdout, stderr, code, err := runCommand(r.Context(), a.tools.path("brief"), []string{"ls"}, home, nil, env, 15*time.Second)
	if err != nil || code != 0 {
		warnings = append(warnings, "Could not list skills: "+firstLine(stderr, err))
	} else {
		for _, line := range strings.Split(strings.TrimSpace(string(stdout)), "\n") {
			name, description, found := strings.Cut(line, "\t")
			if found && validCapabilityName(name) {
				skills = append(skills, workerSkill{Name: name, Description: description, Path: "skills/" + name + "/SKILL.md"})
			}
		}
	}
	listing := func(rel string) []fileEntry {
		files, err := browseHome(home, rel)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				warnings = append(warnings, rel+": "+err.Error())
			}
			return nil
		}
		return files.Entries
	}
	proposals := []string{}
	root, err := withinHome(home, ".agent/learning/proposals")
	if err == nil {
		entries, readErr := os.ReadDir(root)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			warnings = append(warnings, readErr.Error())
		}
		for _, entry := range entries {
			if !entry.IsDir() && entry.Type().IsRegular() && validLearningProposal(entry.Name()) {
				proposals = append(proposals, entry.Name())
			}
		}
	} else {
		warnings = append(warnings, err.Error())
	}
	memories, programs, specialists := listing("state/kv"), listing("tools"), listing("agents")
	writeJSON(w, http.StatusOK, map[string]any{
		"skills": skills, "memory": memories, "tools": programs, "specialists": specialists,
		"learning": map[string]any{"available": a.tools.path("hone") != "", "proposals": proposals}, "warnings": warnings,
	})
}

func validCapabilityName(name string) bool {
	return validSlug(name) && name[0] >= 'a' && name[0] <= 'z' && !strings.HasSuffix(name, "-")
}

func validLearningProposal(name string) bool {
	base, ok := strings.CutSuffix(name, ".json")
	return ok && validID(base)
}

func (a *application) handleShowLearning(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	proposal := r.PathValue("proposal")
	if !validLearningProposal(proposal) {
		writeError(w, http.StatusBadRequest, "learning", "Choose a recorded learning proposal.", "")
		return
	}
	home := a.homeDir(worker.Slug)
	path, err := withinHome(home, ".agent/learning/proposals/"+proposal)
	if err != nil {
		writeError(w, http.StatusBadRequest, "learning", err.Error(), "")
		return
	}
	before, err := readRegularFileLimit(path, 8<<20)
	if err != nil {
		writeError(w, http.StatusNotFound, "learning", err.Error(), "")
		return
	}
	stdout, stderr, code, err := a.tools.run(r.Context(), "agent", []string{"learn", "-show", proposal, home}, "", nil, 30*time.Second)
	if err != nil || code != 0 {
		writeError(w, http.StatusConflict, "learning", firstLine(stderr, err), "")
		return
	}
	if err := checkFileVersion(path, contentSHA256(before)); err != nil {
		writeError(w, http.StatusConflict, "learning", "The proposal changed while it was read. Open it again.", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"operation": "show", "proposal": proposal, "sha256": contentSHA256(before), "output": string(stdout)})
}

func (a *application) handleCreateSkill(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Method      string `json:"method"`
	}
	if err := decodeJSON(r, &in, 40*1024); err != nil {
		writeError(w, http.StatusBadRequest, "json", err.Error(), "")
		return
	}
	in.Name, in.Description, in.Method = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description), strings.TrimSpace(in.Method)
	if !validCapabilityName(in.Name) || in.Description == "" || len(in.Description) > 1024 || in.Method == "" {
		writeError(w, http.StatusBadRequest, "skill", "Give the skill a lowercase name, a short description of when to use it, and its method.", "")
		return
	}
	name, _ := json.Marshal(in.Name)
	description, _ := json.Marshal(in.Description)
	content := []byte("---\nname: " + string(name) + "\ndescription: " + string(description) + "\n---\n\n" + in.Method + "\n")
	if len(content) > 32768 {
		writeError(w, http.StatusBadRequest, "skill", "Keep the skill under 32 KiB; put substantial reference material in supporting files.", "")
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
	home := a.homeDir(worker.Slug)
	dir, err := withinHome(home, "skills/"+in.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "skill", err.Error(), "")
		return
	}
	// Creating a skill never replaces an existing procedure or its resources.
	if err := os.Mkdir(dir, 0o755); err != nil {
		writeError(w, http.StatusConflict, "skill", err.Error(), "Choose a new skill name, or edit the existing skill from the terminal.")
		return
	}
	path := filepath.Join(dir, "SKILL.md")
	rollback := func() { _ = os.Remove(path); _ = os.Remove(dir) }
	if err := writeFileAtomic(path, content, true); err != nil {
		rollback()
		writeError(w, http.StatusInternalServerError, "skill", err.Error(), "")
		return
	}
	receipt := a.checkHome(r.Context(), home)
	if !receipt.Valid {
		rollback()
		writeError(w, http.StatusConflict, "skill", receipt.Message, "Your draft is kept. Fix the skill and try again.")
		return
	}
	worker.Receipt = receipt
	if err := a.store.SaveWorker(worker); err != nil {
		rollback()
		writeError(w, http.StatusInternalServerError, "skill", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"path": "skills/" + in.Name + "/SKILL.md", "receipt": receipt})
}

func (a *application) handleLearning(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Operation string `json:"operation"`
		Skill     string `json:"skill"`
		Session   string `json:"session"`
		Proposal  string `json:"proposal"`
		SHA256    string `json:"sha256"`
	}
	if err := decodeJSON(r, &in, 8192); err != nil {
		writeError(w, http.StatusBadRequest, "json", err.Error(), "")
		return
	}
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	home := a.homeDir(worker.Slug)
	args := []string{"learn"}
	switch in.Operation {
	case "inspect", "prepare":
		if !validCapabilityName(in.Skill) || in.Session == "" || strings.HasPrefix(in.Session, "-") || len(in.Session) > 1024 {
			writeError(w, http.StatusBadRequest, "learning", "Choose a local skill name and a source session from this worker's run history.", "")
			return
		}
		args = append(args, "-into", in.Skill)
		if in.Operation == "inspect" {
			args = append(args, "-why")
		} else {
			in.Proposal = newRequestID("lesson", a.now()) + ".json"
			args = append(args, "-prepare", in.Proposal, "-n", "3")
			if worker.Model != "" {
				args = append(args, "-m", worker.Model)
			}
		}
		args = append(args, home, in.Session)
	case "show", "admit":
		if !validLearningProposal(in.Proposal) {
			writeError(w, http.StatusBadRequest, "learning", "Choose a recorded learning proposal.", "")
			return
		}
		args = append(args, "-"+in.Operation, in.Proposal, home)
	default:
		writeError(w, http.StatusBadRequest, "learning", "Unknown learning operation.", "")
		return
	}
	// These operations share the executor's file lock. Admission cannot race a
	// run or retirement, and the exact proposal shown must still be present.
	a.lifecycleMu.Lock()
	if in.Operation == "prepare" || in.Operation == "admit" {
		if _, err := a.mutableWorker(worker.Slug); err != nil {
			a.lifecycleMu.Unlock()
			writeError(w, http.StatusConflict, "learning", err.Error(), "")
			return
		}
	}
	lock, err := lockExecution(a.store.workerDir(worker.Slug), false)
	a.lifecycleMu.Unlock()
	if err != nil {
		writeError(w, http.StatusConflict, "busy", err.Error(), "")
		return
	}
	defer lock.Close()
	proposalPath := filepath.Join(home, ".agent", "learning", "proposals", in.Proposal)
	var before []byte
	if in.Operation == "show" || in.Operation == "admit" {
		path, pathErr := withinHome(home, ".agent/learning/proposals/"+in.Proposal)
		if pathErr == nil {
			before, pathErr = readRegularFileLimit(path, 8<<20)
		}
		if pathErr != nil || (in.Operation == "admit" && (in.SHA256 == "" || in.SHA256 != contentSHA256(before))) {
			writeError(w, http.StatusConflict, "learning", "The proposal is missing or changed. Open it again before adding the skill.", "")
			return
		}
	}
	stdout, stderr, code, err := a.tools.run(r.Context(), "agent", args, "", nil, 4*time.Minute)
	if err != nil || code != 0 {
		message := firstLine(stderr, err)
		if in.Operation == "inspect" && code == 1 {
			message = "No verified recovery was found in this session. A result passing once is not evidence of a reusable lesson."
		}
		if in.Operation == "prepare" && code == 1 {
			message = "No useful lesson was found to add. The worker's skills are unchanged. Try a run with a clear mistake and correction."
		}
		writeError(w, http.StatusConflict, "learning", message, "")
		return
	}
	if in.Operation == "prepare" {
		before, err = readRegularFileLimit(proposalPath, 8<<20)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "learning", "Agent returned without a readable learning proposal.", "")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"operation": in.Operation, "proposal": in.Proposal, "sha256": contentSHA256(before), "output": string(stdout)})
}
