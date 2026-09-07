package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// The two directory renames are journaled on the same filesystem as the skill.
// Every Hire executor recovers an unfinished swap under its existing kernel
// execution lock before it can use the home. Recovery never invokes a model.
type skillTransaction struct {
	ID              string      `json:"id"`
	RevisionID      string      `json:"revisionID"`
	Skill           string      `json:"skill"`
	BeforeSHA256    string      `json:"beforeSha256"`
	AfterSHA256     string      `json:"afterSha256"`
	Committed       bool        `json:"committed"`
	PriorState      string      `json:"priorState"`
	PriorAppliedAt  *time.Time  `json:"priorAppliedAt,omitempty"`
	PriorRevertedAt *time.Time  `json:"priorRevertedAt,omitempty"`
	PriorReceipt    homeReceipt `json:"priorReceipt"`
}

func syncSkillDir(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
func bundleHashIfPresent(path string) (string, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	bundle, err := readSkillBundle(path)
	return bundle.SHA256, err
}

// Recover an interrupted install when a manager reopens its review, including
// the interval where the installed directory has been moved to its backup.
func (a *application) recoverSkillRead(slug string) error {
	workerDir := a.store.workerDir(slug)
	if _, err := os.Lstat(filepath.Join(workerDir, "skill-transaction.json")); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	lock, err := lockExecution(workerDir, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	a.skillImprovementMu.Lock()
	defer a.skillImprovementMu.Unlock()
	return recoverSkillTransaction(workerDir, a.homeDir(slug))
}

// Caller holds lockExecution(workerDir). Unexpected external edits stop
// recovery instead of overwriting them or running a partially changed skill.
func recoverSkillTransaction(workerDir, home string) error {
	journalPath := filepath.Join(workerDir, "skill-transaction.json")
	var journal skillTransaction
	if err := readJSON(journalPath, &journal); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if !validID(journal.ID) || !validID(journal.RevisionID) || !validCapabilityName(journal.Skill) {
		return errors.New("invalid skill update journal; inspect the worker before continuing")
	}
	live, err := withinHome(home, "skills/"+journal.Skill)
	if err != nil {
		return err
	}
	transaction, err := withinHome(home, ".agent/skill-updates/"+journal.ID)
	if err != nil {
		return err
	}
	current, err := bundleHashIfPresent(live)
	if err != nil {
		return err
	}
	if journal.Committed {
		if current != journal.AfterSHA256 {
			return errors.New("the committed skill changed before update recovery; inspect it before running work")
		}
	} else {
		if current != journal.BeforeSHA256 {
			if current != "" && current != journal.AfterSHA256 {
				return errors.New("skill changed during an interrupted update; recovery will not overwrite it")
			}
			backup := filepath.Join(transaction, "backup")
			hash, err := bundleHashIfPresent(backup)
			if err != nil || hash != journal.BeforeSHA256 {
				return errors.New("the previous skill copy is missing or changed; inspect the interrupted update")
			}
			if current != "" {
				if err := os.Rename(live, filepath.Join(transaction, "uncommitted")); err != nil {
					return err
				}
			}
			if err := os.Rename(backup, live); err != nil {
				return err
			}
			if err := syncSkillDir(filepath.Dir(live)); err != nil {
				return err
			}
			if err := syncSkillDir(transaction); err != nil {
				return err
			}
		}
		revisionDir, err := withinHome(workerDir, "skill-improvements/"+journal.Skill+"/"+journal.RevisionID)
		if err != nil {
			return err
		}
		var revision SkillImprovement
		if err := readJSON(filepath.Join(revisionDir, "revision.json"), &revision); err != nil {
			return err
		}
		revision.State, revision.AppliedAt, revision.RevertedAt = journal.PriorState, journal.PriorAppliedAt, journal.PriorRevertedAt
		revision.Error = "An unfinished installation was rolled back to the previous skill. Review before applying again."
		if err := saveSkillImprovement(revisionDir, revision); err != nil {
			return err
		}
		var worker Worker
		if err := readJSON(filepath.Join(workerDir, "worker.json"), &worker); err != nil {
			return err
		}
		worker.Receipt = journal.PriorReceipt
		if err := writeJSONAtomic(filepath.Join(workerDir, "worker.json"), worker, false); err != nil {
			return err
		}
	}
	if err := os.Remove(journalPath); err != nil {
		return err
	}
	return syncSkillDir(workerDir)
}

func (a *application) installSkillVersion(ctx context.Context, worker Worker, revision SkillImprovement, dir string, revert bool) (SkillImprovement, error) {
	home, workerDir := a.homeDir(worker.Slug), a.store.workerDir(worker.Slug)
	if err := recoverSkillTransaction(workerDir, home); err != nil {
		return revision, err
	}
	source, expected, currentExpected := "candidate", revision.After, revision.Before
	if revert {
		source, expected, currentExpected = "before", revision.Before, revision.After
	}
	live, err := a.skillRoot(worker.Slug, revision.Skill)
	if err != nil {
		return revision, err
	}
	if err := verifySkillBundle(live, currentExpected); err != nil {
		return revision, err
	}
	if err := verifySkillProposal(dir, revision); err != nil {
		return revision, err
	}
	id := newRequestID("skill-swap", a.now())
	transaction, err := withinHome(home, ".agent/skill-updates/"+id)
	if err != nil {
		return revision, err
	}
	if err := os.MkdirAll(transaction, 0700); err != nil {
		return revision, err
	}
	incoming := filepath.Join(transaction, "incoming")
	if err := copySkillBundle(filepath.Join(dir, source), incoming, expected); err != nil {
		return revision, err
	}
	// The live version is checked again after staging; an ordinary CLI editor
	// does not participate in Hire's lock.
	if err := verifySkillBundle(live, currentExpected); err != nil {
		return revision, err
	}
	journal := skillTransaction{ID: id, RevisionID: revision.ID, Skill: revision.Skill, BeforeSHA256: currentExpected.SHA256, AfterSHA256: expected.SHA256, PriorState: revision.State, PriorAppliedAt: revision.AppliedAt, PriorRevertedAt: revision.RevertedAt, PriorReceipt: worker.Receipt}
	journalPath := filepath.Join(workerDir, "skill-transaction.json")
	if err := writeJSONAtomic(journalPath, journal, true); err != nil {
		return revision, err
	}
	if err := syncSkillDir(workerDir); err != nil {
		return revision, err
	}
	restore := func(cause error) (SkillImprovement, error) {
		if err := recoverSkillTransaction(workerDir, home); err != nil {
			return revision, fmt.Errorf("skill update: %v; recovery: %w", cause, err)
		}
		return revision, cause
	}
	if err := os.Rename(live, filepath.Join(transaction, "backup")); err != nil {
		return restore(err)
	}
	if err := syncSkillDir(filepath.Dir(live)); err != nil {
		return restore(err)
	}
	if err := syncSkillDir(transaction); err != nil {
		return restore(err)
	}
	if err := os.Rename(incoming, live); err != nil {
		return restore(err)
	}
	if err := syncSkillDir(filepath.Dir(live)); err != nil {
		return restore(err)
	}
	if err := syncSkillDir(transaction); err != nil {
		return restore(err)
	}
	receipt := a.checkHome(ctx, home)
	if !receipt.Valid {
		return restore(errors.New(receipt.Message))
	}
	if err := verifySkillBundle(live, expected); err != nil {
		return restore(err)
	}
	worker.Receipt = receipt
	if err := a.store.SaveWorker(worker); err != nil {
		return restore(err)
	}
	now := a.now()
	if revert {
		revision.State, revision.RevertedAt = "reverted", &now
	} else {
		revision.State, revision.AppliedAt, revision.RevertedAt = "applied", &now, nil
	}
	revision.Error = ""
	if err := saveSkillImprovement(dir, revision); err != nil {
		return restore(err)
	}
	journal.Committed = true
	if err := writeJSONAtomic(journalPath, journal, false); err != nil {
		return restore(err)
	}
	if err := recoverSkillTransaction(workerDir, home); err != nil {
		return revision, err
	}
	return revision, nil
}

func (a *application) handleApplySkillImprovement(w http.ResponseWriter, r *http.Request) {
	if action := r.PathValue("action"); action != "apply" && action != "revert" {
		writeError(w, 404, "skill", "Unknown skill action.", "")
		return
	}
	var in struct {
		SHA256        string `json:"sha256"`
		CurrentSHA256 string `json:"currentSha256"`
	}
	if err := decodeJSON(r, &in, 2048); err != nil {
		writeError(w, 400, "skill", err.Error(), "")
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
		writeError(w, 409, "busy", err.Error(), "")
		return
	}
	defer lock.Close()
	a.skillImprovementMu.Lock()
	defer a.skillImprovementMu.Unlock()
	if err := recoverSkillTransaction(a.store.workerDir(worker.Slug), a.homeDir(worker.Slug)); err != nil {
		writeError(w, 409, "skill", err.Error(), "")
		return
	}
	revision, dir, err := a.readSkillImprovement(worker.Slug, r.PathValue("skill"), r.PathValue("id"))
	if err != nil || in.SHA256 == "" || in.SHA256 != revision.ProposalSHA256 {
		writeError(w, 409, "skill", "The proposal changed. Open it again before applying.", "")
		return
	}
	revert := r.PathValue("action") == "revert"
	root, err := a.skillRoot(worker.Slug, revision.Skill)
	if err != nil {
		writeError(w, 409, "skill", err.Error(), "")
		return
	}
	current, err := readSkillBundle(root)
	if err != nil {
		writeError(w, 409, "skill", "The installed skill changed. Reopen it before applying or restoring a version.", "")
		return
	}
	// A repeated response after a connection loss reports the already completed
	// action without rewriting a skill or running checks again.
	if (!revert && revision.State == "applied" && current.SHA256 == revision.After.SHA256) || (revert && revision.State == "reverted" && current.SHA256 == revision.Before.SHA256) {
		writeJSON(w, 200, map[string]any{"improvement": revision})
		return
	}
	if current.SHA256 != in.CurrentSHA256 {
		writeError(w, 409, "skill", "The installed skill changed. Reopen it before applying or restoring a version.", "")
		return
	}
	if revert {
		if revision.State != "applied" {
			writeError(w, 409, "skill", "Only an applied improvement can be restored.", "")
			return
		}
	} else {
		if revision.State != "review" && revision.State != "reverted" {
			writeError(w, 409, "skill", "This improvement is not ready to apply.", "")
			return
		}
		if err := skillTestsPassed(revision); err != nil {
			writeError(w, 409, "skill", err.Error(), "")
			return
		}
	}
	revision, err = a.installSkillVersion(r.Context(), worker, revision, dir, revert)
	if err != nil {
		writeError(w, 409, "skill", err.Error(), "Reopen the improvement to inspect its current state.")
		return
	}
	writeJSON(w, 200, map[string]any{"improvement": revision})
}
