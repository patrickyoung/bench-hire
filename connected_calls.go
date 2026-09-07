package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type AppCall struct {
	ID             string          `json:"id"`
	ConnectionID   string          `json:"connectionId"`
	WorkerSlug     string          `json:"workerSlug"`
	RequestID      string          `json:"requestId"`
	GrantVersion   string          `json:"grantVersion"`
	Session        string          `json:"session,omitempty"`
	Source         *UploadRef      `json:"source,omitempty"`
	SourceError    string          `json:"sourceError,omitempty"`
	Resolution     string          `json:"resolution,omitempty"`
	ResolutionNote string          `json:"resolutionNote,omitempty"`
	ResolvedAt     *time.Time      `json:"resolvedAt,omitempty"`
	Kind           string          `json:"kind"`
	Name           string          `json:"name"`
	URI            string          `json:"uri,omitempty"`
	Input          json.RawMessage `json:"input,omitempty"`
	Fingerprint    string          `json:"fingerprint"`
	State          string          `json:"state"`
	Exit           int             `json:"exit"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          string          `json:"error,omitempty"`
	ReviewCommand  string          `json:"reviewCommand,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
	FinishedAt     *time.Time      `json:"finishedAt,omitempty"`
}

func (a *application) appCallsDir(slug string) string {
	return filepath.Join(a.store.workerDir(slug), "app-calls")
}
func (a *application) appCallPath(slug, id string) (string, error) {
	if !validSlug(slug) || !validID(id) {
		return "", errors.New("invalid app call identity")
	}
	return filepath.Join(a.appCallsDir(slug), id, "call.json"), nil
}
func (a *application) saveAppCall(call AppCall) error {
	path, err := a.appCallPath(call.WorkerSlug, call.ID)
	if err != nil {
		return err
	}
	return writeJSONAtomic(path, call, false)
}
func (a *application) appCalls(slug string) ([]AppCall, error) {
	entries, err := os.ReadDir(a.appCallsDir(slug))
	if errors.Is(err, os.ErrNotExist) {
		return []AppCall{}, nil
	}
	if err != nil {
		return nil, err
	}
	calls := []AppCall{}
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		var call AppCall
		if err := readJSON(filepath.Join(a.appCallsDir(slug), entry.Name(), "call.json"), &call); err != nil {
			return nil, err
		}
		calls = append(calls, call)
	}
	slices.SortFunc(calls, func(x, y AppCall) int {
		if compared := x.CreatedAt.Compare(y.CreatedAt); compared != 0 {
			return compared
		}
		return strings.Compare(x.ID, y.ID)
	})
	return calls, nil
}

func (a *application) executeAppCall(ctx context.Context, worker Worker, requestID string, in appCallRequest) appCallResponse {
	fail := func(err error) appCallResponse { return appCallResponse{Exit: 2, Error: err.Error()} }
	// This interprocess lock also excludes the terminal review helper. It is
	// outside the worker's writable home and does not block revocation.
	lockDir := filepath.Join(a.store.workerDir(worker.Slug), "app-locks", in.ConnectionID)
	if !validID(in.ConnectionID) {
		return fail(errors.New("invalid app connection"))
	}
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return fail(err)
	}
	lock, err := lockExecution(lockDir, false)
	if err != nil {
		return fail(errors.New("An app call is already in progress. Inspect app activity before continuing."))
	}
	defer lock.Close()
	home := a.homeDir(worker.Slug)
	grant, err := readAppGrant(home, in.ConnectionID)
	if err != nil {
		return fail(err)
	}
	c, err := a.loadConnectedApp(in.ConnectionID)
	if err != nil {
		return fail(err)
	}
	if !grant.Enabled || !c.Enabled || worker.RetiredAt != nil || worker.RetiringAt != nil {
		return fail(errors.New("App access is disconnected, revoked, or the employee is retired"))
	}
	if in.Operation == "list" {
		return appCallResponse{Data: grantView(grant)}
	}
	if in.Operation == "describe" {
		kind, name, _ := strings.Cut(in.Name, "\x00")
		for _, cap := range grant.Capabilities {
			if cap.Kind == kind && cap.Name == name {
				return appCallResponse{Data: cap}
			}
		}
		return fail(errors.New("This employee has no access to that capability"))
	}
	kind := map[string]string{"run": "tools", "read": "resources", "read-template": "templates"}[in.Operation]
	permission, err := validateAppGrant(grant, worker, c, kind, in.Name)
	if err != nil {
		return fail(err)
	}
	if kind == "tools" || kind == "templates" {
		in.Input, err = canonicalAppJSON(in.Input)
		if err != nil {
			return fail(err)
		}
	}
	if len(in.URI) > 8192 || strings.ContainsAny(in.URI, "\x00\r\n") {
		return fail(errors.New("invalid resource URI"))
	}
	box, err := a.appGrantBox(grant)
	if err != nil {
		return fail(err)
	}
	if err := verifySkillBundle(box, grant.Bundle); err != nil {
		return fail(errors.New("The admitted app programs changed. Review and save this employee's access again."))
	}
	fingerprint := contentSHA256([]byte(strings.Join([]string{requestID, kind, in.Name, in.URI, string(in.Input)}, "\x00")))
	previous, err := a.appCalls(worker.Slug)
	if err != nil {
		return fail(err)
	}
	for _, call := range previous {
		if call.ConnectionID != c.ID {
			continue
		}
		if call.State == "running" || call.State == "uncertain" || call.State == "pending" {
			return appCallResponse{Exit: 125, Call: &call, Error: "An earlier call needs review before this app can be used again. Do not repeat it automatically."}
		}
		if call.Fingerprint == fingerprint {
			return appCallResponse{Exit: call.Exit, Call: &call, Error: "This exact task operation was already recorded; returning its existing outcome without repeating it."}
		}
	}
	call := AppCall{ID: newRequestID("appcall", a.now()), ConnectionID: c.ID, WorkerSlug: worker.Slug, RequestID: requestID, GrantVersion: grant.Version, Kind: kind, Name: in.Name, URI: in.URI, Input: in.Input, Fingerprint: fingerprint, State: "prepared", CreatedAt: a.now()}
	if kind == "tools" {
		call.Session, err = appRequestSession(home, requestID)
		if err != nil {
			return fail(err)
		}
	}
	if kind == "tools" && permission.Mode == "review" {
		call.State, call.Exit = "review", 75
		call.ReviewCommand = strings.Join([]string{shellQuote(a.executable), "app-review", shellQuote(a.dataRoot), shellQuote(worker.Slug), shellQuote(call.ID)}, " ")
		if err := a.saveAppCall(call); err != nil {
			return fail(err)
		}
		return appCallResponse{Exit: 75, Call: &call, Error: "Prepared for manager review. No service operation has run. The manager can inspect app activity and review this exact call with May in a terminal."}
	}
	return a.performAppCall(ctx, worker, c, grant, call, false, nil, nil)
}

func (a *application) performAppCall(ctx context.Context, worker Worker, c ConnectedApp, grant AppGrant, call AppCall, review bool, terminalOut, terminalErr *os.File) appCallResponse {
	path, _ := a.appCallPath(call.WorkerSlug, call.ID)
	dir := filepath.Dir(path)
	box, err := a.appGrantBox(grant)
	if err != nil {
		return appCallResponse{Exit: 2, Error: err.Error()}
	}
	call.State = "running"
	if err := a.saveAppCall(call); err != nil {
		return appCallResponse{Exit: 2, Error: err.Error()}
	}
	var out, errout []byte
	var code int
	if call.Kind == "tools" {
		proposal := filepath.Join(dir, "proposal.json")
		err = writeJSONAtomic(proposal, map[string]any{"version": 1, "connector": call.Name, "input": call.Input}, false)
		policy := filepath.Join(dir, "policy")
		config, _ := a.appDir(c.ID)
		if err == nil {
			script := "#!/bin/sh\nexec " + shellQuote(a.executable) + " app-policy " + shellQuote(filepath.Join(config, "connection.json")) + " " + shellQuote(a.homeDir(worker.Slug)) + " " + shellQuote(path) + " " + shellQuote(filepath.Join(a.store.workerDir(worker.Slug), "worker.json")) + "\n"
			err = writeFileAtomic(policy, []byte(script), false)
		}
		if err == nil {
			err = os.Chmod(policy, 0700)
		}
		if err == nil {
			args := []string{"run", "-job", "hire-" + call.ID, "-policy", policy, "-record", call.Session, "-ask", a.tools.path("ask"), "-timeout", "90s", "-proposal", proposal}
			if review {
				args = append(args, "-may", a.tools.path("may"))
			}
			env := append(connectedEnvironment(c, AppCredentials{}, config), "ACTION_PATH="+filepath.Join(box, "actions"))
			// Action owns the receipts and release boundary. Its stdout is the
			// connector result; May independently opens /dev/tty when required.
			// Terminal review keeps the foreground process group so May can
			// read /dev/tty without being stopped by job control (SIGTTIN).
			out, errout, code, err = runConnectedCommandMode(ctx, a.tools.path("action"), args, a.homeDir(worker.Slug), nil, env, 5*time.Minute, !review)
		}
	} else {
		program := filepath.Join(box, "bin", "read")
		args := []string{call.Name}
		if call.Kind == "templates" {
			program = filepath.Join(box, "bin", "read-template")
			args = append(args, call.URI)
		}
		out, errout, code, err = runConnectedCommand(ctx, program, args, a.homeDir(worker.Slug), call.Input, connectedEnvironment(c, AppCredentials{}, filepath.Dir(box)), 2*time.Minute)
	}
	if terminalOut != nil && len(out) > 0 {
		_, _ = terminalOut.Write(out)
	}
	if terminalErr != nil && len(errout) > 0 {
		_, _ = terminalErr.Write(errout)
	}
	if err != nil {
		// Once the external controller has started, a lost process or bounded
		// output cannot establish whether an operation crossed its boundary.
		code = 125
	}
	call.Exit = code
	call.Error = a.redactAppError(c, connectedFailure(errout, err))
	if len(errout) == 0 && err == nil {
		call.Error = ""
	}
	if len(out) > 0 && json.Valid(out) {
		call.Result = json.RawMessage(out)
	}
	switch code {
	case 0:
		call.State = "succeeded"
	case 1:
		call.State = "failed"
	case 2, 3, 130:
		call.State = "stopped"
	case 75:
		call.State = "pending"
		if review && len(out) == 0 {
			call.State = "review"
		}
	default:
		call.State = "uncertain"
		call.Exit = 125
	}
	if code == 3 && call.Error == "" {
		call.Error = "Action did not authorize this operation. No connector input was released."
	}
	now := a.now()
	call.FinishedAt = &now
	if call.Result != nil {
		sourceRaw, _ := json.MarshalIndent(map[string]any{"service": c.Name, "operation": call.Name, "kind": call.Kind, "input": call.Input, "result": call.Result, "exit": call.Exit, "state": call.State, "requestId": call.RequestID, "callId": call.ID}, "", "  ")
		sourceRaw = a.redactAppData(c, sourceRaw)
		u, sourceErr := a.createAppSource(ctx, worker.Slug, c.ID, c.Name+" — "+call.Name+" result.json", "mcp-results", sourceRaw)
		if sourceErr == nil {
			refs, importErr := a.importUploads(a.homeDir(worker.Slug), worker.Slug, "inputs/app-results/"+call.ID, []string{u.ID})
			if importErr != nil {
				sourceErr = importErr
			} else {
				call.Source = &refs[0]
			}
		}
		if sourceErr != nil {
			call.SourceError = "The operation's outcome is retained, but its citation source needs attention: " + sourceErr.Error()
		}
	}
	if err := a.saveAppCall(call); err != nil {
		return appCallResponse{Exit: 125, Error: "The call outcome could not be saved. Inspect the Action receipts before continuing."}
	}
	public := a.publicAppCall(call)
	return appCallResponse{Exit: call.Exit, Call: &public, Error: call.Error}
}

// Agent documents its home-scoped checkpoint as the current Ask session
// pointer. Action appends to that existing run; Hire does not write Ask events.
func appRequestSession(home, requestID string) (string, error) {
	if !validID(requestID) {
		return "", errors.New("invalid app task identity")
	}
	path, err := withinHome(home, ".agent/checkpoints/"+requestID+".current")
	if err != nil {
		return "", err
	}
	raw, err := readRegularFileLimit(path, 16<<10)
	if err != nil {
		return "", errors.New("The task's Ask session is unavailable; no app operation was released")
	}
	session := strings.TrimSuffix(string(raw), "\n")
	if strings.ContainsAny(session, "\r\n\x00") || !filepath.IsAbs(session) {
		return "", errors.New("invalid task session pointer")
	}
	rel, err := filepath.Rel(filepath.Join(home, ".agent", "runs"), session)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", errors.New("task session is outside this employee's run evidence")
	}
	checked, err := withinHome(home, ".agent/runs/"+filepath.ToSlash(rel))
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(checked)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("task session is not a regular evidence file")
	}
	return checked, nil
}

// This deterministic policy is supplied by the operator, never by the model.
// It binds Action's canonical envelope to a saved call and the current grant.
func runAppPolicy(args []string, stdin io.Reader, stdout io.Writer) int {
	raw, err := io.ReadAll(io.LimitReader(stdin, (16<<10)+1))
	decision, reason, code := "deny", "The current app grant does not authorize this exact call", 3
	if err == nil && len(raw) <= 16<<10 && len(args) == 4 {
		var c ConnectedApp
		var call AppCall
		var worker Worker
		var envelope struct {
			Version   int             `json:"version"`
			Job       string          `json:"job"`
			Connector string          `json:"connector"`
			Path      string          `json:"connector_path"`
			SHA       string          `json:"connector_sha256"`
			Directory string          `json:"directory"`
			Input     json.RawMessage `json:"input"`
		}
		if readJSON(args[0], &c) == nil && readJSON(args[2], &call) == nil && readJSON(args[3], &worker) == nil && json.Unmarshal(raw, &envelope) == nil {
			grant, grantErr := readAppGrant(args[1], c.ID)
			permission, permissionErr := validateAppGrant(grant, worker, c, "tools", call.Name)
			box := filepath.Join(filepath.Dir(args[0]), "grants", worker.Slug, grant.Version, "box")
			input, inputErr := canonicalAppJSON(envelope.Input)
			expectedInput, expectedInputErr := canonicalAppJSON(call.Input)
			connector := filepath.Join(box, "actions", call.Name)
			connectorBytes, fileErr := readRegularFileLimit(connector, 1<<20)
			if grantErr == nil && permissionErr == nil && inputErr == nil && expectedInputErr == nil && fileErr == nil && grant.Version == call.GrantVersion && call.State == "running" && call.Kind == "tools" && envelope.Version == 1 && envelope.Job == "hire-"+call.ID && envelope.Connector == call.Name && envelope.Path == connector && envelope.SHA == "sha256:"+contentSHA256(connectorBytes) && envelope.Directory == args[1] && bytes.Equal(input, expectedInput) && verifySkillBundle(box, grant.Bundle) == nil {
				if permission.Mode == "automatic" {
					decision, reason, code = "allow", "Matches the manager's current app permission and this exact call", 0
				} else {
					decision, reason, code = "review", "The manager requires May review for this app operation", 75
				}
			}
		}
	}
	result := struct {
		Version  int    `json:"version"`
		SHA      string `json:"action_sha256"`
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}{1, "sha256:" + contentSHA256(raw), decision, reason}
	_ = json.NewEncoder(stdout).Encode(result)
	return code
}

func (a *application) handleAppCalls(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	calls, err := a.appCalls(worker.Slug)
	if err != nil {
		writeError(w, 500, "connections", err.Error(), "")
		return
	}
	if len(calls) > 100 {
		calls = calls[len(calls)-100:]
	}
	for i := range calls {
		calls[i].Result = nil
		calls[i].Input = nil
	}
	writeJSON(w, 200, map[string]any{"calls": calls})
}

func (a *application) handleGetAppCall(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	path, err := a.appCallPath(worker.Slug, r.PathValue("call"))
	var call AppCall
	if err != nil || readJSON(path, &call) != nil {
		writeError(w, 404, "connections", "App operation not found.", "")
		return
	}
	public := a.publicAppCall(call)
	dataJSON := public.Result
	var resultFields map[string]json.RawMessage
	if json.Unmarshal(public.Result, &resultFields) == nil && resultFields["structuredContent"] != nil {
		dataJSON = resultFields["structuredContent"]
	}
	writeJSON(w, 200, map[string]any{"call": public, "inputJSON": string(public.Input), "resultDataJSON": string(dataJSON)})
}

func (a *application) handleResolveAppCall(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadMutableWorker(w, r)
	if !ok {
		return
	}
	var in struct {
		Resolution string `json:"resolution"`
		Note       string `json:"note"`
	}
	if err := decodeJSON(r, &in, 12<<10); err != nil || !slices.Contains([]string{"confirmed-effect", "confirmed-no-effect"}, in.Resolution) || strings.TrimSpace(in.Note) == "" || len(in.Note) > 8192 {
		writeError(w, 400, "connections", "Record what you verified in the service and whether the operation took effect.", "")
		return
	}
	path, err := a.appCallPath(worker.Slug, r.PathValue("call"))
	var call AppCall
	if err != nil || readJSON(path, &call) != nil {
		writeError(w, 404, "connections", "App operation not found.", "")
		return
	}
	lock, err := lockExecution(filepath.Join(a.store.workerDir(worker.Slug), "app-locks", call.ConnectionID), false)
	if err != nil {
		writeError(w, 409, "connections", "The operation is still running. Wait for it to finish before recording the outcome.", "")
		return
	}
	defer lock.Close()
	if readJSON(path, &call) != nil || !slices.Contains([]string{"running", "uncertain", "pending"}, call.State) {
		writeError(w, 409, "connections", "This operation is not awaiting an outcome review.", "")
		return
	}
	call.Resolution, call.ResolutionNote, call.State = in.Resolution, strings.TrimSpace(in.Note), "resolved"
	now := a.now()
	call.ResolvedAt = &now
	if err := a.saveAppCall(call); err != nil {
		writeError(w, 500, "connections", err.Error(), "")
		return
	}
	writeJSON(w, 200, map[string]any{"call": call, "message": "Your observation is recorded. The original exit status and receipts are retained. No operation was repeated."})
}

func runAppReview(args []string, stdout, stderr *os.File) int {
	if len(args) != 3 || !validSlug(args[1]) || !validID(args[2]) {
		fmt.Fprintln(stderr, "usage: hire app-review DATA EMPLOYEE CALL")
		return 2
	}
	root, err := filepath.Abs(args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	store, err := newStore(filepath.Join(root, "hire"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	self, _ := os.Executable()
	a := &application{store: store, dataRoot: root, workersRoot: filepath.Join(root, "workers"), tools: newToolset(suiteBinDir(self)), executable: self, now: time.Now}
	path, _ := a.appCallPath(args[1], args[2])
	var call AppCall
	if readJSON(path, &call) != nil || call.State != "review" {
		fmt.Fprintln(stderr, "This call is not awaiting review. Inspect its existing outcome; do not retry it automatically.")
		return 2
	}
	lock, err := lockExecution(filepath.Join(a.store.workerDir(args[1]), "app-locks", call.ConnectionID), false)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	defer lock.Close()
	// Re-read after locking against another terminal or worker.
	if readJSON(path, &call) != nil || call.State != "review" {
		fmt.Fprintln(stderr, "This call has already been handled.")
		return 2
	}
	worker, err := store.Worker(args[1])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	c, err := a.loadConnectedApp(call.ConnectionID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	grant, err := readAppGrant(a.homeDir(worker.Slug), c.ID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	permission, err := validateAppGrant(grant, worker, c, "tools", call.Name)
	if err != nil || grant.Version != call.GrantVersion || permission.Mode != "review" {
		fmt.Fprintln(stderr, "The employee's app access changed; this saved call cannot be released.")
		return 2
	}
	if a.tools.path("may") == "" || a.tools.path("action") == "" || a.tools.path("ask") == "" {
		fmt.Fprintln(stderr, "Action, May and Ask are required to review this call.")
		return 2
	}
	response := a.performAppCall(context.Background(), worker, c, grant, call, true, stdout, stderr)
	if response.Error != "" {
		fmt.Fprintln(stderr, response.Error)
	}
	return response.Exit
}
