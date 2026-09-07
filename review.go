package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// ResultReview records a manager's judgment of particular bytes. It never
// changes Tend's outcome, substitutes for bin/check, or authorizes an effect.
type ResultReview struct {
	Uploads      []UploadRef `json:"uploads,omitempty"`
	ID           string      `json:"id"`
	Sequence     int         `json:"sequence"`
	RequestID    string      `json:"requestId"`
	Decision     string      `json:"decision"`
	Note         string      `json:"note,omitempty"`
	ResultSHA256 string      `json:"resultSha256"`
	JobUpdatedUS int64       `json:"jobUpdatedUs"`
	ReviewedAt   time.Time   `json:"reviewedAt"`
}

func resultDigest(home, id string) (string, error) {
	path, err := withinHome(home, "work/requests/"+id+"/RESULT.md")
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 32<<20 {
		return "", errors.New("the result must be a nonempty regular file of at most 32 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, (32<<20)+1))
	if err != nil || n != info.Size() {
		return "", errors.New("the result changed while it was read; reload it")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *Store) ResultReviews(slug, id string) ([]ResultReview, error) {
	if !validSlug(slug) || !validID(id) {
		return nil, errNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var reviews []ResultReview
	err := readAllJSON(filepath.Join(s.workerDir(slug), "reviews", id), func(path string) error {
		var review ResultReview
		if err := readJSON(path, &review); err != nil {
			return err
		}
		reviews = append(reviews, review)
		return nil
	})
	sort.Slice(reviews, func(i, j int) bool {
		if reviews[i].Sequence != reviews[j].Sequence {
			return reviews[i].Sequence > reviews[j].Sequence
		}
		if reviews[i].ReviewedAt.Equal(reviews[j].ReviewedAt) {
			return reviews[i].ID > reviews[j].ID
		}
		return reviews[i].ReviewedAt.After(reviews[j].ReviewedAt)
	})
	return reviews, err
}

func (s *Store) SaveResultReview(slug string, review *ResultReview) error {
	if !validSlug(slug) || !validID(review.RequestID) || !validID(review.ID) {
		return errors.New("invalid review identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.workerDir(slug), "reviews", review.RequestID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	review.Sequence = len(entries) + 1
	return writeJSONAtomic(filepath.Join(s.workerDir(slug), "reviews", review.RequestID, review.ID+".json"), review, true)
}

func (a *application) handleReviewResult(w http.ResponseWriter, r *http.Request) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	if worker.RetiredAt != nil || worker.RetiringAt != nil {
		writeError(w, http.StatusConflict, "retired", "This worker is retiring or retired. Its results and reviews remain available.", "")
		return
	}
	request, err := a.store.Request(worker.Slug, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "request", "No such task.", "")
		return
	}
	var in struct {
		UploadIDs    []string `json:"uploadIDs"`
		Decision     string   `json:"decision"`
		Note         string   `json:"note"`
		ResultSHA256 string   `json:"resultSha256"`
		JobUpdatedUS int64    `json:"jobUpdatedUs"`
	}
	if err := decodeJSON(r, &in, 16*1024); err != nil {
		writeError(w, http.StatusBadRequest, "json", err.Error(), "")
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if (in.Decision != "accepted" && in.Decision != "changes-requested") || len(in.Note) > 8192 || (in.Decision == "changes-requested" && in.Note == "") {
		writeError(w, http.StatusBadRequest, "review", "Accept the result, or explain what needs to change in at most 8192 characters.", "")
		return
	}
	job, err := a.jobs.Show(r.Context(), request.ID)
	if err != nil || job.Status != "done" {
		writeError(w, http.StatusConflict, "review", "The task must pass its automatic checks before you can accept or review its result.", "Inspect what happened or continue the task.")
		return
	}
	digest, err := requestResultDigest(a.homeDir(worker.Slug), request)
	if err != nil || digest != in.ResultSHA256 || job.UpdatedUS != in.JobUpdatedUS {
		writeError(w, http.StatusConflict, "stale-result", "The result changed since you opened it. Read the latest version before reviewing it.", "Reload the task.")
		return
	}
	home, sourceErr := requestHome(a.homeDir(worker.Slug), request)
	var refs []UploadRef
	if sourceErr == nil {
		refs, sourceErr = a.importUploads(home, worker.Slug, "inputs/uploads", in.UploadIDs)
	}
	if sourceErr != nil {
		writeError(w, 409, "references", sourceErr.Error(), "")
		return
	}
	if err := validUploadIDs(uploadRefIDs(joinUploadRefs(request.Uploads, refs))); err != nil {
		writeError(w, 409, "references", "A revision can use at most 16 original and feedback references combined. Attach fewer files or assign a separate task.", "")
		return
	}
	review := ResultReview{Uploads: refs, ID: newRequestID("review", a.now()), RequestID: request.ID, Decision: in.Decision, Note: in.Note, ResultSHA256: digest, JobUpdatedUS: job.UpdatedUS, ReviewedAt: a.now()}
	reviews, err := a.store.ResultReviews(worker.Slug, request.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "review", err.Error(), "")
		return
	}
	if len(reviews) > 0 {
		previous := reviews[0]
		if previous.Decision == review.Decision && previous.Note == review.Note && slices.Equal(uploadRefIDs(previous.Uploads), uploadRefIDs(review.Uploads)) && previous.ResultSHA256 == digest && previous.JobUpdatedUS == job.UpdatedUS {
			writeJSON(w, http.StatusOK, map[string]any{"review": previous})
			return
		}
	}
	if err := a.store.SaveResultReview(worker.Slug, &review); err != nil {
		writeError(w, http.StatusInternalServerError, "review", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"review": review})
}

// One feedback receipt identifies one revision task. Retrying a lost HTTP
// response reopens that task instead of starting the work a second time.
func (a *application) createRevision(ctx context.Context, slug, requestID, reviewID string) (Request, error) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	worker, err := a.mutableWorker(slug)
	if err != nil {
		return Request{}, err
	}
	if !validID(requestID) || !validID(reviewID) {
		return Request{}, errors.New("choose the feedback you want the worker to address")
	}
	id := "revision-" + contentSHA256([]byte(slug + "/" + requestID + "/" + reviewID))[:40]
	if saved, err := a.store.Request(slug, id); err == nil {
		if saved.RevisionOf != requestID || saved.ReviewID != reviewID {
			return Request{}, errors.New("the saved revision has a conflicting identity")
		}
		return saved, nil
	} else if !errors.Is(err, errNotFound) {
		return Request{}, err
	}
	original, err := a.store.Request(slug, requestID)
	if err != nil {
		return Request{}, err
	}
	reviews, err := a.store.ResultReviews(slug, requestID)
	if err != nil {
		return Request{}, err
	}
	if len(reviews) == 0 || reviews[0].ID != reviewID || reviews[0].Decision != "changes-requested" {
		return Request{}, errors.New("the feedback changed; reopen the task before sending a revision")
	}
	review := reviews[0]
	job, err := a.jobs.Show(ctx, requestID)
	if err != nil || job.Status != "done" || job.UpdatedUS != review.JobUpdatedUS {
		return Request{}, errors.New("the original task changed after your review; inspect it again")
	}
	digest, err := requestResultDigest(a.homeDir(slug), original)
	if err != nil || digest != review.ResultSHA256 {
		return Request{}, errors.New("the original result changed after your review; read it again")
	}
	now := a.now()
	text := fmt.Sprintf("Revise the result of task %s.\n\nOriginal task:\n%s\n\nPrevious result: work/requests/%s/RESULT.md\n\nManager feedback:\n%s\n\nWrite the improved result in this new task's result directory. Preserve the previous result. This feedback applies to this task; it does not change your standing job description or install a skill.", original.ID, original.Text, original.ID, review.Note)
	revision := Request{Uploads: joinUploadRefs(original.Uploads, review.Uploads), ID: id, WorkerSlug: slug, Kind: "revision", Title: "Revise: " + original.Title, Text: text, Check: original.Check, Source: "revision:" + original.ID, RevisionOf: original.ID, ReviewID: reviewID, NotBefore: now, Model: worker.Model, Network: worker.Network, Runs: true, CreatedAt: now}
	revision.Specialist = original.Specialist
	if original.Specialist != "" {
		revision.Network = original.Network
	}
	return revision, a.store.CreateRequest(revision)
}

func (a *application) handleRevision(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ReviewID string `json:"reviewId"`
	}
	if err := decodeJSON(r, &in, 1024); err != nil {
		writeError(w, http.StatusBadRequest, "json", err.Error(), "")
		return
	}
	revision, err := a.createRevision(r.Context(), r.PathValue("slug"), r.PathValue("id"), in.ReviewID)
	if err != nil {
		writeError(w, http.StatusConflict, "revision", err.Error(), "")
		return
	}
	var warnings []string
	if err := a.submitRequest(r.Context(), revision); err != nil {
		warnings = append(warnings, "The revision is saved. Hire will retry queue delivery: "+err.Error())
	}
	writeJSON(w, http.StatusCreated, map[string]any{"request": revision, "warnings": warnings})
}

// Task evidence comes from recorded runs, never from today's job description.
func (a *application) taskEvidence(ctx context.Context, worker Worker, request Request) map[string]any {
	runs, err := readExecutionEvidence(a.store.workerDir(worker.Slug), request.ID)
	if err != nil {
		return map[string]any{"error": fmt.Sprint(err)}
	}
	reviews, err := a.store.ResultReviews(worker.Slug, request.ID)
	if err != nil {
		return map[string]any{"error": fmt.Sprint(err)}
	}
	result := map[string]any{"requestCheck": request.Check, "reviews": reviews, "runs": runs, "recorded": len(runs) > 0}
	if len(runs) == 0 {
		result["scope"] = "No run definition has been recorded for this task. Older tasks may predate this record. The task instructions remain available below."
		return result
	}
	latest := runs[0]
	result["standardCheck"] = latest.CheckSHA256 == contentSHA256([]byte(renderCheckScript(latest.Definition.Checks)))
	result["checks"] = latest.Definition.Checks
	result["scope"] = "These criteria were recorded when the latest run started. Passing automatic checks does not establish accuracy or usefulness; your review is a separate decision."
	if latest.DefinitionStable == nil {
		result["notice"] = "The run has not recorded a final comparison of its definition and check."
	} else if !*latest.DefinitionStable {
		result["notice"] = "The definition or check changed during this run. These are the starting criteria; they cannot prove what was checked at the end."
	}
	return result
}
