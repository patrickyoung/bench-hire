package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type SourceSkillDraft struct {
	InstalledPath   string    `json:"installedPath,omitempty"`
	InstalledSHA256 string    `json:"installedSha256,omitempty"`
	ID              string    `json:"id"`
	WorkerSlug      string    `json:"workerSlug"`
	UploadIDs       []string  `json:"uploadIDs"`
	Goal            string    `json:"goal"`
	Name            string    `json:"name"`
	Description     string    `json:"description"`
	Method          string    `json:"method"`
	State           string    `json:"state"`
	Error           string    `json:"error,omitempty"`
	Session         string    `json:"session"`
	CreatedAt       time.Time `json:"createdAt"`
}

const sourceSkillSchema = `{"type":"object","additionalProperties":false,"required":["name","description","method"],"properties":{"name":{"type":"string"},"description":{"type":"string"},"method":{"type":"string"}}}`

func (a *application) sourceSkillPath(slug, id string) (string, error) {
	if !validSlug(slug) || !validID(id) {
		return "", errors.New("invalid teaching draft")
	}
	return withinHome(a.dataRoot, "teaching/"+slug+"/"+id+".json")
}
func (a *application) handleDraftSourceSkill(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadMutableWorker(w, r)
	if !ok {
		return
	}
	var in struct {
		Goal      string   `json:"goal"`
		UploadIDs []string `json:"uploadIDs"`
	}
	if err := decodeJSON(r, &in, 16<<10); err != nil {
		writeError(w, 400, "teaching", err.Error(), "")
		return
	}
	if a.tools.paths["cite"] == "" {
		writeError(w, 409, "teaching", "Cite is required to check the proposed skill’s source links. Install it in the selected Bench suite.", "")
		return
	}
	if strings.TrimSpace(in.Goal) == "" || len(in.UploadIDs) == 0 {
		writeError(w, 400, "teaching", "Describe the skill to teach and attach at least one ready source.", "")
		return
	}
	if _, err := a.uploadEvidence(r.Context(), worker.Slug, in.UploadIDs); err != nil {
		writeError(w, 409, "teaching", err.Error(), "")
		return
	}
	draft := SourceSkillDraft{ID: newRequestID("teaching", a.now()), WorkerSlug: worker.Slug, Goal: in.Goal, UploadIDs: in.UploadIDs, State: "drafting", CreatedAt: a.now()}
	draft.Session = filepath.Join(a.askDir, draft.ID+".jsonl")
	path, err := a.sourceSkillPath(worker.Slug, draft.ID)
	a.sourceSkillMu.Lock()
	defer a.sourceSkillMu.Unlock()
	if err == nil {
		err = writeJSONAtomic(path, draft, true)
	}
	if err != nil {
		writeError(w, 500, "teaching", err.Error(), "")
		return
	}
	if a.sourceSkillActive == nil {
		a.sourceSkillActive = map[string]bool{}
	}
	a.sourceSkillActive[draft.ID] = true
	go a.prepareSourceSkill(worker, draft, path)
	writeJSON(w, 202, map[string]any{"draft": draft})
}
func (a *application) prepareSourceSkill(worker Worker, draft SourceSkillDraft, path string) {
	ctx, cancel := context.WithTimeout(a.backgroundContext(), 5*time.Minute)
	defer cancel()
	ctx = withSources(ctx, worker.Slug, draft.UploadIDs)
	err := a.ensureModelProved(ctx, worker.Model)
	if err == nil {
		err = os.MkdirAll(a.askDir, 0700)
	}
	schema := filepath.Join(a.askDir, "source-skill-schema.json")
	if err == nil {
		err = writeFileAtomic(schema, []byte(sourceSkillSchema), false)
	}
	var raw []byte
	if err == nil {
		input, _ := json.Marshal(map[string]any{"goal": draft.Goal, "job": worker.Purpose})
		raw, err = a.runBuilderAsk(ctx, worker.Model, draft.Session, "source-skill-schema.json", "Draft a practical Brief skill taught from the supplied materials. Return a lowercase hyphenated name, a short description of when to use it, and a Markdown method with steps, inputs, limits, and ways to check the output. Cite the materials beside the procedures they support. Distinguish procedures stated in the sources from inferred or suggested steps. Label assumptions and include unresolved questions in the method for the manager to review. This is a proposed method for a manager to review, not a skill verified by successful execution or a Hone recovery. Never invent training evidence or grant tools or access.", input, 32<<10)
	}
	if err == nil {
		var result struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Method      string `json:"method"`
		}
		err = json.Unmarshal(raw, &result)
		if err == nil && (!validCapabilityName(result.Name) || strings.TrimSpace(result.Description) == "" || len(result.Description) > 1024 || strings.TrimSpace(result.Method) == "" || len(result.Method) > 28000) {
			err = errors.New("the skill draft was incomplete or exceeded the skill limits")
		}
		if err == nil {
			// The review copy includes its own evidence; installation copies it again
			// into the admitted skill, never relying on mutable controller paths.
			var refs []UploadRef
			refs, err = a.importUploads(a.dataRoot, worker.Slug, "teaching/"+worker.Slug+"/"+draft.ID+"-sources", draft.UploadIDs)
			if err == nil {
				err = a.checkSourceCitations(ctx, a.dataRoot, refs, []byte(result.Method))
			}
			if err == nil {
				draft.Name, draft.Description, draft.Method = result.Name, result.Description, result.Method
			}
		}
	}
	if err != nil {
		draft.State, draft.Error = "failed", err.Error()
	} else {
		draft.State = "ready"
	}
	a.sourceSkillMu.Lock()
	defer a.sourceSkillMu.Unlock()
	_ = writeJSONAtomic(path, draft, false)
	delete(a.sourceSkillActive, draft.ID)
}
func (a *application) handleGetSourceSkills(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	a.sourceSkillMu.Lock()
	defer a.sourceSkillMu.Unlock()
	entries, _ := os.ReadDir(filepath.Join(a.dataRoot, "teaching", worker.Slug))
	drafts := []map[string]any{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path, err := a.sourceSkillPath(worker.Slug, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			continue
		}
		var draft SourceSkillDraft
		if readJSON(path, &draft) != nil {
			continue
		}
		if draft.State == "drafting" && !a.sourceSkillActive[draft.ID] {
			draft.State, draft.Error = "failed", "Drafting was interrupted. Start a new draft explicitly."
			_ = writeJSONAtomic(path, draft, false)
		}
		raw, err := readRegularFileLimit(path, 64<<10)
		if err != nil {
			continue
		}
		drafts = append(drafts, map[string]any{"draft": draft, "sha256": contentSHA256(raw)})
	}
	writeJSON(w, 200, map[string]any{"drafts": drafts})
}

func (a *application) handleCheckSourceCitations(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	request, err := a.store.Request(worker.Slug, r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "citations", "Task not found.", "")
		return
	}
	if len(request.Uploads) == 0 {
		writeJSON(w, 200, map[string]any{"state": "none"})
		return
	}
	home, err := requestHome(a.homeDir(worker.Slug), request)
	var candidate []byte
	if err == nil {
		var path string
		path, err = withinHome(home, "work/requests/"+request.ID+"/RESULT.md")
		if err == nil {
			candidate, err = readRegularFileLimit(path, 4<<20)
		}
	}
	if err == nil {
		err = os.MkdirAll(a.askDir, 0700)
	}
	if err == nil {
		err = a.checkSourceCitations(r.Context(), home, request.Uploads, candidate)
	}
	state, message := "valid", "Citation links match the supplied source records. This checks identity, not factual accuracy or coverage."
	if err != nil {
		state, message = "needs-review", err.Error()
	}
	writeJSON(w, 200, map[string]any{"state": state, "message": message, "resultSha256": contentSHA256(candidate)})
}
