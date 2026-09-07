package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
)

// Managers can correct proposed source or test files. Every edit invalidates
// the previous test receipt. Installed and historical versions stay immutable.
func (a *application) handleEditSkillImprovement(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SHA256  string `json:"sha256"`
		Version string `json:"version"`
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &in, (512<<10)+2048); err != nil {
		writeError(w, 400, "skill", err.Error(), "")
		return
	}
	if !skillBundlePath(in.Path) || (in.Version != "candidate" && in.Version != "checks") || !isSkillText([]byte(in.Content)) || len(in.Content) > 512<<10 {
		writeError(w, 400, "skill", "Choose a proposed text file to edit, under 512 KiB.", "")
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
		writeError(w, 409, "skill", "Reopen a proposal that is awaiting review before editing.", "")
		return
	}
	if err := verifySkillProposal(dir, revision); err != nil {
		writeError(w, 409, "skill", err.Error(), "")
		return
	}
	for _, ref := range skillUploadReferences(revision.Uploads) {
		if in.Version == "candidate" && (in.Path == ref.OriginalPath || in.Path == ref.TextPath || in.Path == ref.EvidencePath) {
			writeError(w, 409, "skill", "Retained source originals and evidence cannot be edited. Upload corrected source material instead.", "")
			return
		}
	}
	path, err := withinHome(dir, in.Version+"/"+in.Path)
	if err != nil {
		writeError(w, 400, "skill", err.Error(), "")
		return
	}
	before, err := readRegularFileLimit(path, 512<<10)
	if err != nil || !isSkillText(before) {
		writeError(w, 409, "skill", "Only existing proposed text files can be edited here.", "")
		return
	}
	info, err := os.Lstat(path)
	if err != nil {
		writeError(w, 409, "skill", err.Error(), "")
		return
	}
	if in.Version == "candidate" && in.Path == "SKILL.md" && len(in.Content) > 32768 {
		writeError(w, 400, "skill", "Keep SKILL.md under 32 KiB.", "")
		return
	}
	if err := a.checkSkillCitations(r.Context(), dir, revision, revision.Plan.Summary+"\n"+in.Content); err != nil {
		writeError(w, 409, "citations", err.Error(), "")
		return
	}
	if err := writeFileAtomic(path, []byte(in.Content), false); err != nil {
		writeError(w, 500, "skill", err.Error(), "")
		return
	}
	restore := func(cause error) {
		_ = writeFileAtomic(path, before, false)
		_ = os.Chmod(path, info.Mode().Perm())
		writeError(w, 409, "skill", cause.Error(), "")
	}
	if err := os.Chmod(path, info.Mode().Perm()); err != nil {
		restore(err)
		return
	}
	if in.Version == "checks" {
		found := false
		for i := range revision.Plan.CheckFiles {
			if revision.Plan.CheckFiles[i].Path == in.Path {
				revision.Plan.CheckFiles[i].Content = in.Content
				found = true
			}
		}
		if !found {
			restore(errors.New("the check file is not in the reviewed plan"))
			return
		}
		harness, err := readSkillBundle(filepath.Join(dir, "checks"))
		if err != nil {
			restore(err)
			return
		}
		revision.ChecksSHA256 = harness.SHA256
	} else {
		changed := false
		for i := range revision.Plan.Changes {
			change := &revision.Plan.Changes[i]
			if change.Path == in.Path {
				change.Operation, change.Content, change.UploadID, change.SourcePath = "write", in.Content, "", ""
				changed = true
			}
		}
		if !changed {
			revision.Plan.Changes = append(revision.Plan.Changes, SkillFileChange{Path: in.Path, Operation: "write", Content: in.Content, Executable: info.Mode().Perm()&0111 != 0, Reason: "Edited directly by the manager during review."})
		}
		revision.After, err = readSkillBundle(filepath.Join(dir, "candidate"))
		if err != nil {
			restore(err)
			return
		}
	}
	// Retain a manager-edit marker in the reviewed explanation.
	revision.Plan.Summary += "\n\nManager edited " + in.Version + "/" + in.Path + " during review."
	raw, _ := json.Marshal(revision.Plan)
	if len(raw) > 2<<20 {
		restore(errors.New("the edited proposal exceeds 2 MiB"))
		return
	}
	revision.ProposalSHA256 = skillProposalSHA(revision)
	revision.TestedSHA256, revision.Error = "", ""
	revision.Results = []SkillCheckResult{}
	if err := saveSkillImprovement(dir, revision); err != nil {
		restore(err)
		return
	}
	writeJSON(w, 200, map[string]any{"improvement": revision})
}
