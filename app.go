package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type application struct {
	store       *Store
	jobs        Jobs
	tools       *toolset
	runner      *Runner
	assets      fs.FS
	dataRoot    string
	workersRoot string
	askDir      string
	executable  string
	codexCache  string
	location    *time.Location
	now         func() time.Time
	model       string
	token       string
	host        string

	runtimeMu       sync.Mutex
	runtimeCache    RuntimeReport
	runtimeCachedAt time.Time
}

type apiError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	NextAction string `json:"nextAction,omitempty"`
}

type requestView struct {
	Request
	State        string     `json:"state"`
	JobStatus    string     `json:"jobStatus,omitempty"`
	Exit         *int       `json:"exit,omitempty"`
	Attempts     int        `json:"attempts"`
	ResultExists bool       `json:"resultExists"`
	Summary      string     `json:"summary,omitempty"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	Children     int        `json:"children,omitempty"`
}

type routineView struct {
	Routine
	Cadence string `json:"cadence"`
}

type workerView struct {
	Worker
	Home      string        `json:"home"`
	Routines  []routineView `json:"routines"`
	Requests  []requestView `json:"requests"`
	Queued    int           `json:"queued"`
	Running   int           `json:"running"`
	Done      int           `json:"done"`
	Attention int           `json:"attention"`
}

type bootstrapResponse struct {
	Runtime   RuntimeReport `json:"runtime"`
	Workers   []workerView  `json:"workers"`
	Attention []requestView `json:"attention"`
	Runner    runnerStatus  `json:"runner"`
	Settings  Settings      `json:"settings"`
	Token     string        `json:"token"`
	Now       time.Time     `json:"now"`
}

var attentionStates = map[string]bool{"unknown": true, "unfinished": true, "broken": true, "failed": true, "timed-out": true, "boundary": true}

func (a *application) homeDir(slug string) string { return filepath.Join(a.workersRoot, slug) }

func (a *application) defaultModel() string {
	if settings, err := a.store.Settings(); err == nil && strings.TrimSpace(settings.Model) != "" {
		return strings.TrimSpace(settings.Model)
	}
	return a.model
}

func (a *application) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/bootstrap", a.handleBootstrap)
	mux.HandleFunc("GET /api/runtime", a.handleRuntime)
	mux.HandleFunc("POST /api/model/prove", a.handleProveModel)
	mux.HandleFunc("POST /api/settings", a.handleSettings)
	mux.HandleFunc("POST /api/runner", a.handleRunner)
	mux.HandleFunc("GET /api/workers", a.handleListWorkers)
	mux.HandleFunc("POST /api/workers", a.handleCreateWorker)
	mux.HandleFunc("GET /api/workers/{slug}", a.handleWorker)
	mux.HandleFunc("POST /api/workers/{slug}/enabled", a.handleWorkerEnabled)
	mux.HandleFunc("POST /api/workers/{slug}/update", a.handleWorkerUpdate)
	mux.HandleFunc("DELETE /api/workers/{slug}", a.handleRetireWorker)
	mux.HandleFunc("POST /api/workers/{slug}/requests", a.handleIntake)
	mux.HandleFunc("GET /api/workers/{slug}/requests/{id}", a.handleRequest)
	mux.HandleFunc("POST /api/workers/{slug}/requests/{id}/{action}", a.handleRequestAction)
	mux.HandleFunc("POST /api/workers/{slug}/routines", a.handleCreateRoutine)
	mux.HandleFunc("POST /api/workers/{slug}/routines/{id}/{action}", a.handleRoutineAction)
	mux.HandleFunc("GET /api/workers/{slug}/files", a.handleFiles)
	mux.HandleFunc("PUT /api/workers/{slug}/files", a.handleWriteFile)
	mux.HandleFunc("DELETE /api/workers/{slug}/files", a.handleDeleteFile)
	mux.HandleFunc("GET /api/workers/{slug}/definition", a.handleDefinition)
	mux.HandleFunc("PUT /api/workers/{slug}/definition", a.handleWriteDefinition)
	mux.HandleFunc("PUT /api/workers/{slug}/checks", a.handleWriteChecks)
	mux.HandleFunc("POST /api/workers/{slug}/checks/suggest", a.handleSuggestChecks)
	mux.HandleFunc("GET /api/workers/{slug}/history", a.handleHistory)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not-found", "No such API route.", "")
	})
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(a.assets)))
	mux.HandleFunc("/", a.handleIndex)
	return a.guard(mux)
}

// guard enforces the local boundary: the configured host only, custom
// headers on every mutation, and conservative response headers.
func (a *application) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.hostAllowed(r.Host) {
			http.Error(w, "unexpected host", http.StatusMisdirectedRequest)
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				token := r.Header.Get("X-Hire-Token")
				if subtle.ConstantTimeCompare([]byte(token), []byte(a.token)) != 1 {
					writeError(w, http.StatusForbidden, "token", "This request did not carry the local session token. Reload the page.", "")
					return
				}
				if origin := r.Header.Get("Origin"); origin != "" && !a.originAllowed(origin) {
					writeError(w, http.StatusForbidden, "origin", "Cross-origin requests are refused.", "")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *application) hostAllowed(host string) bool {
	if host == a.host {
		return true
	}
	name, port, err := net.SplitHostPort(host)
	if err != nil {
		return false
	}
	_, wantPort, err := net.SplitHostPort(a.host)
	if err != nil || port != wantPort {
		return false
	}
	if strings.EqualFold(name, "localhost") {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}

func (a *application) originAllowed(origin string) bool {
	origin = strings.TrimPrefix(strings.TrimPrefix(origin, "http://"), "https://")
	return a.hostAllowed(origin)
}

func (a *application) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.Contains(r.URL.Path, ".") && r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(a.assets, "index.html")
	if err != nil {
		http.Error(w, "index unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (a *application) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	views, attention, err := a.workerViews(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "workers", err.Error(), "")
		return
	}
	settings, _ := a.store.Settings()
	if settings.Model == "" {
		settings.Model = a.model
	}
	writeJSON(w, http.StatusOK, bootstrapResponse{Runtime: a.inspectRuntime(ctx), Workers: views, Attention: attention, Runner: a.runner.Status(), Settings: settings, Token: a.token, Now: a.now()})
}

func (a *application) handleRuntime(w http.ResponseWriter, r *http.Request) {
	a.runtimeMu.Lock()
	a.runtimeCachedAt = time.Time{}
	a.runtimeMu.Unlock()
	writeJSON(w, http.StatusOK, a.inspectRuntime(r.Context()))
}

func (a *application) handleProveModel(w http.ResponseWriter, r *http.Request) {
	proof, err := a.proveModel(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, "model", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proof": proof, "model": a.modelReadiness()})
}

func (a *application) handleSettings(w http.ResponseWriter, r *http.Request) {
	var in Settings
	if err := decodeJSON(r, &in, 4096); err != nil {
		writeError(w, http.StatusBadRequest, "json", err.Error(), "")
		return
	}
	in.Model = strings.TrimSpace(in.Model)
	if in.Model != "" {
		if err := validateModel(in.Model); err != nil {
			writeError(w, http.StatusBadRequest, "model", err.Error(), "")
			return
		}
	}
	if err := a.store.SaveSettings(in); err != nil {
		writeError(w, http.StatusInternalServerError, "settings", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": in, "model": a.modelReadiness()})
}

func (a *application) handleRunner(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paused bool `json:"paused"`
	}
	if err := decodeJSON(r, &in, 1024); err != nil {
		writeError(w, http.StatusBadRequest, "json", err.Error(), "")
		return
	}
	a.runner.SetPaused(in.Paused)
	writeJSON(w, http.StatusOK, a.runner.Status())
}

func (a *application) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	views, _, err := a.workerViews(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "workers", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workers": views})
}

func (a *application) handleCreateWorker(w http.ResponseWriter, r *http.Request) {
	var in createWorkerRequest
	if err := decodeJSON(r, &in, 256*1024); err != nil {
		writeError(w, http.StatusBadRequest, "json", err.Error(), "")
		return
	}
	worker, routines, err := a.createWorker(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusBadRequest, "create", err.Error(), "")
		return
	}
	view, err := a.workerView(r.Context(), worker, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "worker", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"worker": view, "routines": routines})
}

func (a *application) loadWorker(w http.ResponseWriter, r *http.Request) (Worker, bool) {
	worker, err := a.store.Worker(r.PathValue("slug"))
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "not-found", "No such worker.", "")
		return Worker{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "worker", err.Error(), "")
		return Worker{}, false
	}
	return worker, true
}

func (a *application) handleWorker(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	view, err := a.workerView(r.Context(), worker, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "worker", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *application) handleWorkerEnabled(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &in, 1024); err != nil {
		writeError(w, http.StatusBadRequest, "json", err.Error(), "")
		return
	}
	if in.Enabled && worker.CheckState != "valid" {
		receipt := a.checkHome(r.Context(), a.homeDir(worker.Slug))
		worker.Receipt = receipt
		if !receipt.Valid {
			worker.CheckState = "invalid"
			worker.CheckMessage = receipt.Message
			_ = a.store.SaveWorker(worker)
			writeError(w, http.StatusConflict, "check", "agent check still rejects this home: "+receipt.Message, "Fix the definition files, then try again.")
			return
		}
		worker.CheckState = "valid"
		worker.CheckMessage = ""
	}
	worker.Enabled = in.Enabled
	if err := a.store.SaveWorker(worker); err != nil {
		writeError(w, http.StatusInternalServerError, "worker", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, worker)
}

// handleWorkerUpdate changes Hire's record of a worker: display name, card
// summary, model, and network grant. Recorded requests keep the model they
// were created with; the definition files have their own editor.
func (a *application) handleWorkerUpdate(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	var in struct {
		Name    *string `json:"name,omitempty"`
		Purpose *string `json:"purpose,omitempty"`
		Model   *string `json:"model,omitempty"`
		Network *bool   `json:"network,omitempty"`
	}
	if err := decodeJSON(r, &in, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "json", err.Error(), "")
		return
	}
	if in.Model != nil {
		model := strings.TrimSpace(*in.Model)
		if model != "" {
			if err := validateModel(model); err != nil {
				writeError(w, http.StatusBadRequest, "model", err.Error(), "")
				return
			}
		}
		worker.Model = model
	}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" || len(name) > 120 {
			writeError(w, http.StatusBadRequest, "name", "A worker needs a name of at most 120 characters.", "")
			return
		}
		worker.Name = name
	}
	if in.Purpose != nil {
		purpose := strings.TrimSpace(*in.Purpose)
		if purpose == "" || len(purpose) > 8192 {
			writeError(w, http.StatusBadRequest, "purpose", "The summary must be between 1 and 8192 characters.", "")
			return
		}
		worker.Purpose = purpose
	}
	if in.Network != nil {
		worker.Network = *in.Network
	}
	if err := a.store.SaveWorker(worker); err != nil {
		writeError(w, http.StatusInternalServerError, "worker", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, worker)
}

// handleRetireWorker keeps the home (it is evidence) but moves it out of the
// active roots and forgets Hire's index of it.
func (a *application) handleRetireWorker(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	jobs, _ := a.jobs.List(r.Context())
	for _, job := range jobs {
		if job.Cwd == a.homeDir(worker.Slug) && job.Status == "ready" {
			_ = a.jobs.Cancel(r.Context(), job.ID)
		}
	}
	retired := filepath.Join(a.dataRoot, "retired")
	if err := os.MkdirAll(retired, 0o700); err != nil {
		writeError(w, http.StatusInternalServerError, "retire", err.Error(), "")
		return
	}
	stamp := a.now().UTC().Format("20060102-150405")
	if err := os.Rename(a.homeDir(worker.Slug), filepath.Join(retired, worker.Slug+"-"+stamp)); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusInternalServerError, "retire", err.Error(), "")
		return
	}
	if err := os.Rename(a.store.workerDir(worker.Slug), filepath.Join(retired, worker.Slug+"-"+stamp+".hire")); err != nil {
		writeError(w, http.StatusInternalServerError, "retire", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"retired": worker.Slug, "movedTo": retired})
}

func (a *application) handleIntake(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	var in intakeRequest
	if err := decodeJSON(r, &in, 256*1024); err != nil {
		writeError(w, http.StatusBadRequest, "json", err.Error(), "")
		return
	}
	response, err := a.intake(r.Context(), worker, in)
	if err != nil {
		writeError(w, http.StatusBadRequest, "intake", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (a *application) handleRequest(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	request, err := a.store.Request(worker.Slug, r.PathValue("id"))
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "not-found", "No such request.", "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "request", err.Error(), "")
		return
	}
	ctx := r.Context()
	all, err := a.store.Requests(worker.Slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "request", err.Error(), "")
		return
	}
	views, err := a.requestViews(ctx, worker, all)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "request", err.Error(), "")
		return
	}
	var view requestView
	var children []requestView
	for _, v := range views {
		if v.ID == request.ID {
			view = v
		}
		if v.ParentID == request.ID {
			children = append(children, v)
		}
	}
	payload := map[string]any{"request": view, "children": children, "worker": worker}
	if job, err := a.jobs.Show(ctx, request.ID); err == nil {
		payload["job"] = job
		if attempts, err := a.jobs.Attempts(ctx, request.ID); err == nil {
			payload["attempts"] = attempts
		}
	}
	if plan, err := a.store.Plan(worker.Slug, request.ID); err == nil {
		payload["plan"] = plan
	} else if request.ParentID != "" {
		if plan, err := a.store.Plan(worker.Slug, request.ParentID); err == nil {
			payload["plan"] = plan
		}
	}
	if result, err := browseHome(a.homeDir(worker.Slug), "work/requests/"+request.ID+"/RESULT.md"); err == nil {
		payload["result"] = result
	}
	payload["requestFile"] = renderRequestFile(request)
	payload["argv"] = append([]string{"agent"}, agentArgs(request, a.homeDir(worker.Slug))...)
	writeJSON(w, http.StatusOK, payload)
}

func (a *application) handleRequestAction(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	request, err := a.store.Request(worker.Slug, r.PathValue("id"))
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "not-found", "No such request.", "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "request", err.Error(), "")
		return
	}
	ctx := r.Context()
	switch r.PathValue("action") {
	case "retry":
		err = a.jobs.Retry(ctx, request.ID)
	case "resolve":
		var in struct {
			Decision string `json:"decision"`
		}
		if err := decodeJSON(r, &in, 1024); err != nil {
			writeError(w, http.StatusBadRequest, "json", err.Error(), "")
			return
		}
		err = a.jobs.Resolve(ctx, request.ID, in.Decision)
	case "cancel":
		err = a.jobs.Cancel(ctx, request.ID)
	case "rerun":
		now := a.now()
		fresh := Request{ID: newRequestID("req", now), WorkerSlug: worker.Slug, Kind: "request", Title: request.Title, Text: request.Text, Check: request.Check, Source: "rerun:" + request.ID, NotBefore: now, Model: worker.Model, Network: worker.Network, Runs: true, CreatedAt: now}
		if err := a.store.CreateRequest(fresh); err != nil {
			writeError(w, http.StatusInternalServerError, "rerun", err.Error(), "")
			return
		}
		if err := a.submitRequest(ctx, fresh); err != nil {
			writeError(w, http.StatusInternalServerError, "rerun", err.Error(), "")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"request": fresh})
		return
	default:
		writeError(w, http.StatusNotFound, "not-found", "Unknown request action.", "")
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, "job", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *application) handleCreateRoutine(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	var in routineInput
	if err := decodeJSON(r, &in, 256*1024); err != nil {
		writeError(w, http.StatusBadRequest, "json", err.Error(), "")
		return
	}
	routine, err := a.buildRoutine(worker.Slug, in, "user", a.now())
	if err != nil {
		writeError(w, http.StatusBadRequest, "routine", err.Error(), "")
		return
	}
	if err := a.store.SaveRoutine(routine); err != nil {
		writeError(w, http.StatusInternalServerError, "routine", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusCreated, routineView{Routine: routine, Cadence: describeCadence(routine.Every, routine.At, routine.Weekday)})
}

func (a *application) handleRoutineAction(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	routine, err := a.store.Routine(worker.Slug, r.PathValue("id"))
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "not-found", "No such routine.", "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "routine", err.Error(), "")
		return
	}
	ctx := r.Context()
	now := a.now()
	switch r.PathValue("action") {
	case "enable", "disable":
		routine.Enabled = r.PathValue("action") == "enable"
		if routine.Enabled && !routine.NextDue.After(now) {
			routine.NextDue = nextDue(routine.Every, routine.At, routine.Weekday, now, a.location)
		}
		err = a.store.SaveRoutine(routine)
	case "run":
		if !worker.Enabled || worker.CheckState != "valid" {
			writeError(w, http.StatusConflict, "worker", "This worker is not accepting work.", "")
			return
		}
		var req Request
		req, err = a.queueRoutine(ctx, worker, routine, now, now)
		if err == nil {
			routine.LastQueued = &now
			routine.LastRequest = req.ID
			err = a.store.SaveRoutine(routine)
		}
		if err == nil {
			writeJSON(w, http.StatusCreated, map[string]any{"request": req})
			return
		}
	case "delete":
		err = a.store.DeleteRoutine(worker.Slug, routine.ID)
	case "update":
		var in routineInput
		if err := decodeJSON(r, &in, 256*1024); err != nil {
			writeError(w, http.StatusBadRequest, "json", err.Error(), "")
			return
		}
		updated, buildErr := a.buildRoutine(worker.Slug, in, routine.Source, now)
		if buildErr != nil {
			writeError(w, http.StatusBadRequest, "routine", buildErr.Error(), "")
			return
		}
		routine.Title, routine.Instructions, routine.Check = updated.Title, updated.Instructions, updated.Check
		routine.Every, routine.At, routine.Weekday = updated.Every, updated.At, updated.Weekday
		routine.NextDue = updated.NextDue
		err = a.store.SaveRoutine(routine)
	default:
		writeError(w, http.StatusNotFound, "not-found", "Unknown routine action.", "")
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, "routine", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, routineView{Routine: routine, Cadence: describeCadence(routine.Every, routine.At, routine.Weekday)})
}

func (a *application) handleFiles(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	view, err := browseHome(a.homeDir(worker.Slug), r.URL.Query().Get("path"))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeError(w, status, "files", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *application) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &in, fileWriteLimit+4096); err != nil {
		writeError(w, http.StatusBadRequest, "json", err.Error(), "")
		return
	}
	if err := writeHomeFile(a.homeDir(worker.Slug), in.Path, in.Content); err != nil {
		writeError(w, http.StatusBadRequest, "files", err.Error(), "")
		return
	}
	view, err := browseHome(a.homeDir(worker.Slug), in.Path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "files", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *application) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	if err := deleteHomeFile(a.homeDir(worker.Slug), r.URL.Query().Get("path")); err != nil {
		writeError(w, http.StatusBadRequest, "files", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *application) handleDefinition(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	home := a.homeDir(worker.Slug)
	files := map[string]string{}
	for _, name := range definitionFiles {
		data, err := os.ReadFile(filepath.Join(home, name))
		if err == nil {
			files[name] = string(data)
		}
	}
	check, _ := os.ReadFile(filepath.Join(home, "bin", "check"))
	checks, err := readWorkerChecks(home)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "checks", err.Error(), "Repair CHECKS.json before editing acceptance checks.")
		return
	}
	proof, proved, _ := a.store.ModelProof()
	assistantReady := worker.Model != "" && proved && proof.OK && proof.Model == worker.Model
	stdout, _, _, _ := a.tools.run(r.Context(), "agent", []string{"show", home}, "", nil, 30*time.Second)
	show := string(stdout)
	if len(show) > 64*1024 {
		show = show[:64*1024] + "\n[truncated]"
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files, "check": string(check), "checks": checks, "show": show, "receipt": worker.Receipt, "home": home, "checkAssistant": map[string]any{"ready": assistantReady, "model": worker.Model}})
}

func (a *application) handleWriteChecks(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	var in struct {
		Checks []WorkerCheck `json:"checks"`
	}
	if err := decodeJSON(r, &in, 128*1024); err != nil {
		writeError(w, http.StatusBadRequest, "json", err.Error(), "")
		return
	}
	checks, err := normalizeWorkerChecks(in.Checks)
	if err != nil {
		writeError(w, http.StatusBadRequest, "checks", err.Error(), "")
		return
	}
	home := a.homeDir(worker.Slug)
	if err := writeWorkerChecks(home, checks); err != nil {
		writeError(w, http.StatusInternalServerError, "checks", err.Error(), "")
		return
	}
	receipt := a.checkHome(r.Context(), home)
	worker.Receipt = receipt
	if receipt.Valid {
		worker.CheckState = "valid"
		worker.CheckMessage = ""
	} else {
		worker.CheckState = "invalid"
		worker.CheckMessage = receipt.Message
	}
	if err := a.store.SaveWorker(worker); err != nil {
		writeError(w, http.StatusInternalServerError, "checks", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"worker": worker, "receipt": receipt, "checks": checks, "check": renderCheckScript(checks)})
}

func (a *application) handleSuggestChecks(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	var in struct {
		Guidance string `json:"guidance"`
	}
	if err := decodeJSON(r, &in, 16*1024); err != nil {
		writeError(w, http.StatusBadRequest, "json", err.Error(), "")
		return
	}
	suggestion, err := a.suggestWorkerChecks(r.Context(), worker, strings.TrimSpace(in.Guidance))
	if err != nil {
		writeError(w, http.StatusBadRequest, "check-assistant", err.Error(), "Prove this worker's model in Setup, or add a structured check manually.")
		return
	}
	writeJSON(w, http.StatusOK, suggestion)
}

func (a *application) handleWriteDefinition(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	var in struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &in, 512*1024); err != nil {
		writeError(w, http.StatusBadRequest, "json", err.Error(), "")
		return
	}
	if !isDefinitionFile(in.Name) {
		writeError(w, http.StatusBadRequest, "definition", "Only the Markdown definition files can be edited here.", "")
		return
	}
	home := a.homeDir(worker.Slug)
	if err := writeFileAtomic(filepath.Join(home, in.Name), []byte(in.Content), false); err != nil {
		writeError(w, http.StatusInternalServerError, "definition", err.Error(), "")
		return
	}
	receipt := a.checkHome(r.Context(), home)
	worker.Receipt = receipt
	if receipt.Valid {
		worker.CheckState = "valid"
		worker.CheckMessage = ""
	} else {
		worker.CheckState = "invalid"
		worker.CheckMessage = receipt.Message
	}
	if err := a.store.SaveWorker(worker); err != nil {
		writeError(w, http.StatusInternalServerError, "definition", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"worker": worker, "receipt": receipt})
}

func (a *application) handleHistory(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	stdout, stderr, code, err := a.tools.run(r.Context(), "agent", []string{"history", a.homeDir(worker.Slug), "ls"}, "", nil, 30*time.Second)
	if err != nil || code != 0 {
		writeError(w, http.StatusBadGateway, "history", "agent history: "+firstLine(stderr, err), "")
		return
	}
	var entries []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(stdout)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) == nil {
			entries = append(entries, entry)
		} else {
			entries = append(entries, map[string]any{"line": line})
		}
	}
	if len(entries) > 100 {
		entries = entries[len(entries)-100:]
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// workerViews joins Hire's records with Tend's job list exactly once per
// request, so a page with many workers costs one tend list.
func (a *application) workerViews(ctx context.Context) ([]workerView, []requestView, error) {
	workers, err := a.store.Workers()
	if err != nil {
		return nil, nil, err
	}
	jobs, err := a.jobs.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	index := map[string]Job{}
	for _, job := range jobs {
		index[job.ID] = job
	}
	views := []workerView{}
	var attention []requestView
	for _, worker := range workers {
		view, err := a.workerView(ctx, worker, index)
		if err != nil {
			return nil, nil, err
		}
		for _, req := range view.Requests {
			if attentionStates[req.State] {
				attention = append(attention, req)
			}
		}
		views = append(views, view)
	}
	return views, attention, nil
}

func (a *application) workerView(ctx context.Context, worker Worker, index map[string]Job) (workerView, error) {
	requests, err := a.store.Requests(worker.Slug)
	if err != nil {
		return workerView{}, err
	}
	var reqViews []requestView
	if index == nil {
		reqViews, err = a.requestViews(ctx, worker, requests)
	} else {
		reqViews, err = a.requestViewsWith(ctx, worker, requests, index)
	}
	if err != nil {
		return workerView{}, err
	}
	routines, err := a.store.Routines(worker.Slug)
	if err != nil {
		return workerView{}, err
	}
	view := workerView{Worker: worker, Home: a.homeDir(worker.Slug), Routines: []routineView{}, Requests: reqViews}
	for _, routine := range routines {
		view.Routines = append(view.Routines, routineView{Routine: routine, Cadence: describeCadence(routine.Every, routine.At, routine.Weekday)})
	}
	if view.Requests == nil {
		view.Requests = []requestView{}
	}
	for _, req := range reqViews {
		switch req.State {
		case "queued", "scheduled", "waiting":
			view.Queued++
		case "running":
			view.Running++
		case "done":
			view.Done++
		}
		if attentionStates[req.State] {
			view.Attention++
		}
	}
	return view, nil
}

func (a *application) requestViews(ctx context.Context, worker Worker, requests []Request) ([]requestView, error) {
	jobs, err := a.jobs.List(ctx)
	if err != nil {
		return nil, err
	}
	index := map[string]Job{}
	for _, job := range jobs {
		index[job.ID] = job
	}
	return a.requestViewsWith(ctx, worker, requests, index)
}

func (a *application) requestViewsWith(ctx context.Context, worker Worker, requests []Request, index map[string]Job) ([]requestView, error) {
	now := a.now()
	home := a.homeDir(worker.Slug)
	children := map[string]int{}
	for _, req := range requests {
		if req.ParentID != "" {
			children[req.ParentID]++
		}
	}
	views := make([]requestView, 0, len(requests))
	for _, req := range requests {
		view := requestView{Request: req, UpdatedAt: req.CreatedAt, Children: children[req.ID]}
		resultPath := filepath.Join(home, "work", "requests", req.ID, "RESULT.md")
		if info, err := os.Stat(resultPath); err == nil && info.Size() > 0 {
			view.ResultExists = true
			view.Summary = resultSummary(resultPath)
		}
		job, hasJob := index[req.ID]
		var lastExit *int
		if hasJob {
			view.JobStatus = job.Status
			view.UpdatedAt = time.UnixMicro(job.UpdatedUS)
			if job.Status == "failed" || job.Status == "done" || job.Status == "unknown" || job.Status == "running" {
				if attempts, err := a.jobs.Attempts(ctx, req.ID); err == nil {
					view.Attempts = len(attempts)
					if len(attempts) > 0 {
						last := attempts[len(attempts)-1]
						lastExit = last.Exit
						if !last.StartedAt.IsZero() {
							started := last.StartedAt
							view.StartedAt = &started
						}
					}
				}
			}
		}
		view.Exit = lastExit
		var jobPtr *Job
		if hasJob {
			jobPtr = &job
		}
		view.State = deriveState(req, jobPtr, lastExit, now)
		views = append(views, view)
	}
	return views, nil
}

func deriveState(r Request, job *Job, lastExit *int, now time.Time) string {
	if !r.Runs {
		return "planned"
	}
	if job == nil {
		return "unsubmitted"
	}
	switch job.Status {
	case "ready":
		if job.NotBeforeUS > now.UnixMicro() {
			return "scheduled"
		}
		return "queued"
	case "running", "waiting", "unknown", "cancelled", "done":
		return job.Status
	case "failed":
		if lastExit != nil {
			switch *lastExit {
			case 2:
				return "unfinished"
			case 1:
				return "broken"
			case 124:
				return "timed-out"
			case 125:
				return "boundary"
			}
		}
		return "failed"
	}
	return job.Status
}

func resultSummary(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "#- "))
		if line != "" {
			if len(line) > 200 {
				line = line[:200] + "…"
			}
			return line
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message, next string) {
	writeJSON(w, status, map[string]any{"error": apiError{Code: code, Message: message, NextAction: next}})
}

func decodeJSON(r *http.Request, v any, limit int64) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > limit {
		return fmt.Errorf("request body is larger than %d bytes", limit)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return errors.New("request body is empty")
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON: %v", err)
	}
	return nil
}
