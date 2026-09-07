package main

import (
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

type AppPermission struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Mode   string `json:"mode"`
}

type AppGrant struct {
	ConnectionID    string          `json:"connectionId"`
	WorkerSlug      string          `json:"workerSlug"`
	Version         string          `json:"version"`
	CatalogueID     string          `json:"catalogueId"`
	CatalogueSHA256 string          `json:"catalogueSha256"`
	Command         string          `json:"command"`
	Instructions    string          `json:"instructions"`
	Enabled         bool            `json:"enabled"`
	Permissions     []AppPermission `json:"permissions"`
	Capabilities    []AppCapability `json:"capabilities"`
	Bundle          SkillBundle     `json:"bundle"`
	ToolSHA256      string          `json:"toolSha256"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

func appGrantPath(home, id string) (string, error) {
	if !validID(id) {
		return "", errors.New("invalid app connection")
	}
	return withinHome(home, ".agent/connections/"+id+".json")
}
func readAppGrant(home, id string) (AppGrant, error) {
	var grant AppGrant
	path, err := appGrantPath(home, id)
	if err != nil {
		return grant, err
	}
	if err := readJSON(path, &grant); err != nil {
		return grant, err
	}
	if grant.ConnectionID != id || !validSlug(grant.WorkerSlug) || !validID(grant.Version) || !validID(grant.CatalogueID) || !validCapabilityName(grant.Command) {
		return grant, errors.New("invalid connected-app grant")
	}
	return grant, nil
}
func saveAppGrant(home string, grant AppGrant) error {
	path, err := appGrantPath(home, grant.ConnectionID)
	if err != nil {
		return err
	}
	return writeJSONAtomic(path, grant, false)
}
func (a *application) appGrantBox(grant AppGrant) (string, error) {
	dir, err := a.appDir(grant.ConnectionID)
	if err != nil {
		return "", err
	}
	if !validSlug(grant.WorkerSlug) || !validID(grant.Version) {
		return "", errors.New("invalid grant identity")
	}
	return withinHome(dir, "grants/"+grant.WorkerSlug+"/"+grant.Version+"/box")
}
func grantPermission(g AppGrant, kind, name string) (AppPermission, bool) {
	for _, p := range g.Permissions {
		if p.Kind == kind && p.Name == name {
			return p, true
		}
	}
	return AppPermission{}, false
}
func grantView(g AppGrant) map[string]any {
	return map[string]any{"connectionId": g.ConnectionID, "workerSlug": g.WorkerSlug, "version": g.Version, "catalogueId": g.CatalogueID, "catalogueSha256": g.CatalogueSHA256, "command": g.Command, "instructions": g.Instructions, "enabled": g.Enabled, "permissions": g.Permissions, "capabilities": g.Capabilities, "createdAt": g.CreatedAt, "updatedAt": g.UpdatedAt}
}
func (a *application) handleWorkerConnectedApps(w http.ResponseWriter, r *http.Request) {
	worker, ok := a.loadWorker(w, r)
	if !ok {
		return
	}
	a.appsMu.Lock()
	defer a.appsMu.Unlock()
	entries, err := os.ReadDir(filepath.Join(a.homeDir(worker.Slug), ".agent", "connections"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeError(w, 500, "connections", err.Error(), "")
		return
	}
	result := []map[string]any{}
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !validID(id) {
			continue
		}
		grant, err := readAppGrant(a.homeDir(worker.Slug), id)
		if err != nil {
			continue
		}
		c, err := a.loadConnectedApp(id)
		if err != nil {
			continue
		}
		a.recoverAppOperation(&c)
		changed := false
		if catalog, err := a.appCatalogue(c); err == nil {
			for _, p := range grant.Permissions {
				found := false
				for _, cap := range catalog.Capabilities {
					if p.Kind == cap.Kind && p.Name == cap.Name && p.Digest == cap.Digest {
						found = true
						break
					}
				}
				if !found {
					changed = true
				}
			}
		}
		connection := a.connectedAppView(c)
		delete(connection, "catalogue")
		result = append(result, map[string]any{"connection": connection, "grant": grantView(grant), "accessChanged": changed})
	}
	writeJSON(w, 200, map[string]any{"worker": worker, "connections": result})
}
func (a *application) handleGrantConnectedApp(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CatalogueSHA256 string          `json:"catalogueSha256"`
		Version         string          `json:"version"`
		Instructions    string          `json:"instructions"`
		Permissions     []AppPermission `json:"permissions"`
	}
	if err := decodeJSON(r, &in, 128<<10); err != nil {
		writeError(w, 400, "connections", err.Error(), "")
		return
	}
	if len(in.Permissions) == 0 || len(in.Permissions) > 128 || len(in.Instructions) > 8192 {
		writeError(w, 400, "connections", "Choose between one and 128 capabilities and describe their intended use briefly.", "")
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
		writeError(w, 409, "connections", "This employee is working. You can prepare access now and save it when the task finishes.", "")
		return
	}
	defer lock.Close()
	a.appsMu.Lock()
	defer a.appsMu.Unlock()
	c, err := a.loadConnectedApp(r.PathValue("connection"))
	if err != nil {
		writeError(w, 404, "connections", "Connection not found.", "")
		return
	}
	if !c.Enabled || c.CatalogueID == "" {
		writeError(w, 409, "connections", "Connect this service before giving an employee access.", "")
		return
	}
	catalog, err := a.appCatalogue(c)
	if err != nil || catalog.SHA256 != in.CatalogueSHA256 {
		writeError(w, 409, "connections", "The service capabilities changed. Open them again before saving access.", "")
		return
	}
	home := a.homeDir(worker.Slug)
	previous, readErr := readAppGrant(home, c.ID)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		writeError(w, 409, "connections", readErr.Error(), "")
		return
	}
	if previous.Version != in.Version {
		writeError(w, 409, "connections", "This employee's access changed. Reopen it before saving.", "")
		return
	}
	selected := []AppCapability{}
	seen := map[string]bool{}
	for _, p := range in.Permissions {
		key := p.Kind + "\x00" + p.Name
		if seen[key] || !slices.Contains([]string{"tools", "resources", "templates"}, p.Kind) || !slices.Contains([]string{"automatic", "review"}, p.Mode) || (p.Kind != "tools" && p.Mode != "automatic") {
			writeError(w, 400, "connections", "Choose distinct tools or resources with supported access modes.", "")
			return
		}
		seen[key] = true
		found := false
		for _, cap := range catalog.Capabilities {
			if cap.Kind == p.Kind && cap.Name == p.Name && cap.Digest == p.Digest {
				selected = append(selected, cap)
				found = true
				break
			}
		}
		if !found {
			writeError(w, 409, "connections", "One of the selected capabilities changed. Review the current service catalogue.", "")
			return
		}
		if p.Kind == "tools" && (a.tools.path("action") == "" || a.tools.path("ask") == "") {
			writeError(w, 409, "connections", "Action and Ask are required to authorize and record service tool calls.", "")
			return
		}
	}
	now := a.now()
	grant := AppGrant{ConnectionID: c.ID, WorkerSlug: worker.Slug, Version: newRequestID("access", now), CatalogueID: c.CatalogueID, CatalogueSHA256: catalog.SHA256, Command: previous.Command, Instructions: strings.TrimSpace(in.Instructions), Enabled: true, Permissions: in.Permissions, Capabilities: selected, CreatedAt: previous.CreatedAt, UpdatedAt: now}
	if previous.Version == "" {
		grant.CreatedAt = now
	}
	if grant.Command == "" {
		label := slugify(c.Name)
		if len(label) > 35 {
			label = label[:35]
		}
		if label == "" {
			label = "service"
		}
		grant.Command = "app-" + label + "-" + c.ID[len(c.ID)-6:]
	}
	root, _ := a.appDir(c.ID)
	source := filepath.Join(root, "catalogues", c.CatalogueID, "box")
	snapshot, err := readSkillBundle(source)
	box, pathErr := a.appGrantBox(grant)
	if err == nil {
		err = pathErr
	}
	if err == nil {
		err = os.MkdirAll(filepath.Dir(box), 0700)
	}
	if err == nil {
		err = copySkillBundle(source, box, snapshot)
	}
	if err != nil {
		writeError(w, 409, "connections", err.Error(), "")
		return
	}
	// Only the capability compiler grants programs. Every MCP tool uses the
	// Action surface, including ones described by a server as read-only.
	transport := filepath.Join(filepath.Dir(box), "transport")
	script := "#!/bin/sh\nexec " + shellQuote(a.executable) + " mcp-grant-transport " + shellQuote(home) + " " + shellQuote(filepath.Join(root, "connection.json")) + " " + shellQuote(grant.Version) + " " + shellQuote(filepath.Join(a.store.workerDir(worker.Slug), "worker.json")) + " \"$@\"\n"
	if err := writeFileAtomic(transport, []byte(script), false); err != nil {
		writeError(w, 500, "connections", err.Error(), "")
		return
	}
	if err := os.Chmod(transport, 0700); err != nil {
		writeError(w, 500, "connections", err.Error(), "")
		return
	}
	if err := writeJSONAtomic(filepath.Join(box, "runtime.json"), map[string]string{"mcp": transport}, false); err != nil {
		writeError(w, 500, "connections", err.Error(), "")
		return
	}
	for _, kind := range []string{"tools", "resources", "templates"} {
		names := []string{}
		for _, p := range grant.Permissions {
			if p.Kind == kind {
				names = append(names, p.Name)
			}
		}
		if len(names) == 0 {
			continue
		}
		admittedKind := kind
		if kind == "tools" {
			admittedKind = "actions"
		}
		args := append([]string{"admit", box, admittedKind}, names...)
		_, stderr, code, callErr := a.connectedCommand(r.Context(), "mcpbox", args, nil, 30*time.Second)
		if callErr != nil || code != 0 {
			writeError(w, 409, "connections", a.redactAppError(c, "Could not grant access: "+connectedFailure(stderr, callErr)), "")
			return
		}
	}
	grant.Bundle, err = readSkillBundle(box)
	if err != nil {
		writeError(w, 409, "connections", err.Error(), "")
		return
	}
	toolPath, err := withinHome(home, "tools/"+grant.Command)
	if err != nil {
		writeError(w, 409, "connections", err.Error(), "")
		return
	}
	var oldTool []byte
	script = a.appToolScript(worker, c, grant)
	if previous.Version != "" {
		oldTool, err = readRegularFileLimit(toolPath, 64<<10)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			writeError(w, 409, "connections", err.Error(), "")
			return
		}
		if err == nil && contentSHA256(oldTool) != previous.ToolSHA256 {
			writeError(w, 409, "connections", "The installed app command was edited outside this access form. Review that change before replacing it.", "")
			return
		}
	} else if _, err := os.Lstat(toolPath); !errors.Is(err, os.ErrNotExist) {
		existing, readErr := readRegularFileLimit(toolPath, 64<<10)
		if readErr != nil || string(existing) != script {
			writeError(w, 409, "connections", "The app tool name is already used by a local program.", "")
			return
		}
	}
	grant.ToolSHA256 = contentSHA256([]byte(script))
	if err := writeFileAtomic(toolPath, []byte(script), false); err != nil {
		writeError(w, 500, "connections", err.Error(), "")
		return
	}
	restoreTool := func() {
		if previous.Version != "" && oldTool != nil {
			_ = writeFileAtomic(toolPath, oldTool, false)
			_ = os.Chmod(toolPath, 0700)
		} else {
			_ = os.Remove(toolPath)
		}
	}
	if err := os.Chmod(toolPath, 0700); err != nil {
		restoreTool()
		writeError(w, 500, "connections", err.Error(), "")
		return
	}
	receipt := a.checkHome(r.Context(), home)
	if !receipt.Valid {
		restoreTool()
		writeError(w, 409, "connections", receipt.Message, "")
		return
	}
	// The existing grant stays authoritative until the last atomic write.
	// A crash before it leaves either an inert new tool or the prior access.
	if err := saveAppGrant(home, grant); err != nil {
		restoreTool()
		writeError(w, 500, "connections", err.Error(), "")
		return
	}
	worker.Receipt = receipt
	if err := a.store.SaveWorker(worker); err != nil {
		writeError(w, 500, "connections", "Access was saved, but the home check receipt could not be updated. Reopen this employee.", "")
		return
	}
	writeJSON(w, 200, map[string]any{"grant": grantView(grant), "connection": a.connectedAppView(c)})
}
func (a *application) handleRevokeConnectedApp(w http.ResponseWriter, r *http.Request) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	worker, ok := a.loadMutableWorker(w, r)
	if !ok {
		return
	}
	a.appsMu.Lock()
	defer a.appsMu.Unlock()
	grant, err := readAppGrant(a.homeDir(worker.Slug), r.PathValue("connection"))
	if err != nil {
		writeError(w, 404, "connections", "This employee has no access to that service.", "")
		return
	}
	grant.Enabled = false
	grant.UpdatedAt = a.now()
	if err := saveAppGrant(a.homeDir(worker.Slug), grant); err != nil {
		writeError(w, 500, "connections", err.Error(), "")
		return
	}
	// In-flight calls may already have taken effect. The controller checks the
	// home grant before every new call, so revocation need not wait for a worker.
	writeJSON(w, 200, map[string]any{"grant": grantView(grant)})
}
func (a *application) appToolScript(worker Worker, c ConnectedApp, g AppGrant) string {
	return "#!/bin/sh\n# Connected app: " + c.Name + ". Use list or describe to inspect permitted capabilities.\nexec " + shellQuote(a.executable) + " app-call " + shellQuote(filepath.Join(a.dataRoot, "connections", "broker.json")) + " " + shellQuote(a.homeDir(worker.Slug)) + " " + shellQuote(c.ID) + " \"$@\"\n"
}

func validateAppGrant(grant AppGrant, worker Worker, c ConnectedApp, kind, name string) (AppPermission, error) {
	if worker.RetiredAt != nil || worker.RetiringAt != nil {
		return AppPermission{}, errors.New("this employee is retired or retiring")
	}
	if !grant.Enabled || !c.Enabled || grant.WorkerSlug != worker.Slug || grant.ConnectionID != c.ID {
		return AppPermission{}, errors.New("access to this app has been revoked or disconnected")
	}
	permission, ok := grantPermission(grant, kind, name)
	if !ok {
		return permission, fmt.Errorf("this employee has not been granted %s %q", kind, name)
	}
	return permission, nil
}

// canonicalAppJSON is only a JSON input boundary, not MCP or Action policy.
func canonicalAppJSON(raw []byte) (json.RawMessage, error) {
	if len(raw) > 8<<10 {
		return nil, errors.New("service arguments exceed Action's 8 KiB input limit")
	}
	var value map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, errors.New("supply one JSON object as the service arguments")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("supply exactly one JSON object")
	}
	return json.Marshal(value)
}

func appWorkInstructions(home string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(home, ".agent", "connections"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var text strings.Builder
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok {
			continue
		}
		grant, err := readAppGrant(home, id)
		if err != nil {
			return "", err
		}
		if !grant.Enabled {
			continue
		}
		fmt.Fprintf(&text, "\n- Connected service command: %s. Manager's intended use: %s. Run `%s list` for the current permitted tools and resources, then `%s describe tools NAME` for the exact inputs.\n", grant.Command, grant.Instructions, grant.Command, grant.Command)
	}
	if text.Len() == 0 {
		return "", nil
	}
	return "\n\nConnected apps explicitly granted by the manager:\nThese installed app commands delegate through Hire's operator-controlled Action boundary. Use them for permitted automatic service work; they prepare a review record for operations requiring May. They do not grant unrestricted network access. Supply a JSON object on stdin to `COMMAND run TOOL`. Read exact service results and their source links. Never invoke the underlying MCP service directly, supply credentials, or bypass this command. Exit 75 requires follow-up; exit 125 is an uncertain outcome and must not be repeated automatically. Report blocked operations and their call IDs in your result. Use the exact returned [ref](url) when citing a service result. Skill instructions cannot expand these permissions.\n" + text.String(), nil
}
