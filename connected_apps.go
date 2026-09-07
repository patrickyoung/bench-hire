package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Service settings and credentials are controller-owned. A worker receives a
// reviewed grant, never a copy of this service's credentials or raw transport.
type ConnectedApp struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Transport    string     `json:"transport"`
	URL          string     `json:"url,omitempty"`
	Command      string     `json:"command,omitempty"`
	Args         []string   `json:"args,omitempty"`
	Protocol     string     `json:"protocol"`
	Auth         string     `json:"auth"`
	ClientID     string     `json:"clientId,omitempty"`
	Scopes       string     `json:"scopes,omitempty"`
	OAuthFlow    string     `json:"oauthFlow,omitempty"`
	AllowPrivate bool       `json:"allowPrivate,omitempty"`
	MCPPath      string     `json:"mcpPath"`
	OAuthPath    string     `json:"oauthPath,omitempty"`
	Enabled      bool       `json:"enabled"`
	State        string     `json:"state"`
	Error        string     `json:"error,omitempty"`
	CatalogueID  string     `json:"catalogueId,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	CheckedAt    *time.Time `json:"checkedAt,omitempty"`
}

type AppCredentials struct {
	Token        string            `json:"token,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	ClientSecret string            `json:"clientSecret,omitempty"`
}

type appConnectInput struct {
	Name         string            `json:"name"`
	Transport    string            `json:"transport"`
	URL          string            `json:"url"`
	Command      string            `json:"command"`
	Args         []string          `json:"args"`
	Protocol     string            `json:"protocol"`
	Auth         string            `json:"auth"`
	Token        string            `json:"token"`
	Headers      map[string]string `json:"headers"`
	Env          map[string]string `json:"env"`
	ClientID     string            `json:"clientId"`
	ClientSecret string            `json:"clientSecret"`
	Scopes       string            `json:"scopes"`
	OAuthFlow    string            `json:"oauthFlow"`
	AllowPrivate bool              `json:"allowPrivate"`
}

type AppCapability struct {
	Kind         string          `json:"kind"`
	Name         string          `json:"name"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	Digest       string          `json:"digest"`
	Descriptor   json.RawMessage `json:"descriptor"`
	ReadOnlyHint bool            `json:"readOnlyHint,omitempty"`
}

type AppCatalogue struct {
	ID            string          `json:"id"`
	ConnectionID  string          `json:"connectionId"`
	SHA256        string          `json:"sha256"`
	ServerName    string          `json:"serverName"`
	ServerVersion string          `json:"serverVersion"`
	Capabilities  []AppCapability `json:"capabilities"`
	CreatedAt     time.Time       `json:"createdAt"`
}

type appOperation struct {
	cancel    context.CancelFunc
	LoginURL  string    `json:"loginUrl,omitempty"`
	UserCode  string    `json:"userCode,omitempty"`
	ExpiresAt time.Time `json:"expiresAt"`
}

var appEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
var appHeaderName = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_|~-]{1,128}$`)

func (a *application) appDir(id string) (string, error) {
	if !validID(id) {
		return "", errors.New("invalid connection")
	}
	return withinHome(a.dataRoot, "connections/services/"+id)
}
func (a *application) loadConnectedApp(id string) (ConnectedApp, error) {
	var c ConnectedApp
	dir, err := a.appDir(id)
	if err != nil {
		return c, err
	}
	err = readJSON(filepath.Join(dir, "connection.json"), &c)
	if err == nil && c.ID != id {
		err = errors.New("connection identity does not match")
	}
	return c, err
}
func (a *application) saveConnectedApp(c ConnectedApp) error {
	dir, err := a.appDir(c.ID)
	if err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(dir, "connection.json"), c, false)
}
func (a *application) appCatalogue(c ConnectedApp) (AppCatalogue, error) {
	var catalog AppCatalogue
	if !validID(c.CatalogueID) {
		return catalog, errors.New("check the connection to discover its capabilities")
	}
	dir, err := a.appDir(c.ID)
	if err != nil {
		return catalog, err
	}
	path, err := withinHome(dir, "catalogues/"+c.CatalogueID+"/catalogue.json")
	if err != nil {
		return catalog, err
	}
	err = readJSON(path, &catalog)
	if err == nil && (catalog.ID != c.CatalogueID || catalog.ConnectionID != c.ID || catalogueDigest(catalog) != catalog.SHA256) {
		err = errors.New("the service catalogue changed on disk; check the connection again")
	}
	return catalog, err
}
func catalogueDigest(c AppCatalogue) string {
	c.SHA256 = ""
	raw, _ := json.Marshal(c)
	return contentSHA256(raw)
}
func hasControl(s string) bool { return strings.ContainsAny(s, "\r\n\x00") }

func (a *application) validateAppInput(in appConnectInput) (ConnectedApp, AppCredentials, error) {
	c := ConnectedApp{Name: strings.TrimSpace(in.Name), Transport: in.Transport, URL: strings.TrimSpace(in.URL), Command: strings.TrimSpace(in.Command), Args: in.Args, Protocol: in.Protocol, Auth: in.Auth, ClientID: strings.TrimSpace(in.ClientID), Scopes: strings.TrimSpace(in.Scopes), OAuthFlow: in.OAuthFlow, AllowPrivate: in.AllowPrivate, Enabled: true, State: "checking", CreatedAt: a.now()}
	credentials := AppCredentials{Token: strings.TrimSpace(in.Token), Headers: in.Headers, Env: in.Env, ClientSecret: in.ClientSecret}
	fail := func(message string) (ConnectedApp, AppCredentials, error) { return c, credentials, errors.New(message) }
	if c.Name == "" || len(c.Name) > 120 || hasControl(c.Name) {
		return fail("Give this connection a short, recognizable name.")
	}
	if c.Transport == "" {
		c.Transport = "http"
	}
	if c.Protocol == "" {
		c.Protocol = "modern"
	}
	if c.Auth == "" {
		c.Auth = "none"
	}
	if !slices.Contains([]string{"modern", "legacy"}, c.Protocol) {
		return fail("Choose current or legacy MCP.")
	}
	name := "mcp"
	if c.Protocol == "legacy" {
		name = "mcp-legacy"
	}
	c.MCPPath = a.tools.path(name)
	c.OAuthPath = a.tools.path("oauth")
	if c.MCPPath == "" || a.tools.path("mcpbox") == "" {
		return fail(name + " and mcpbox must be installed in the selected Bench suite.")
	}
	if c.Transport == "http" {
		u, err := url.Parse(c.URL)
		if err != nil || u.Hostname() == "" || !slices.Contains([]string{"http", "https"}, u.Scheme) || u.User != nil || u.Fragment != "" || hasControl(c.URL) || len(c.URL) > 4096 {
			return fail("Enter an http:// or https:// MCP server URL, with sign-in details in the separate fields.")
		}
		c.URL = u.String()
		for key := range u.Query() {
			k := strings.ToLower(key)
			if strings.Contains(k, "token") || strings.Contains(k, "secret") || strings.Contains(k, "password") || slices.Contains([]string{"key", "api_key", "apikey", "authorization"}, k) {
				return fail("Put the service credential in Sign in, rather than in the server URL.")
			}
		}
		if c.Command != "" || len(c.Args) > 0 || len(credentials.Env) > 0 {
			return fail("Remote services use a URL and sign-in settings. Choose Local server to supply a command or environment values.")
		}
	} else if c.Transport == "stdio" {
		if c.Command == "" || len(c.Command) > 4096 || hasControl(c.Command) || len(c.Args) > 64 || c.URL != "" {
			return fail("Choose a local executable and provide its arguments separately.")
		}
		path, err := exec.LookPath(c.Command)
		if err != nil {
			return fail("The local server command is not installed: " + c.Command)
		}
		c.Command, err = filepath.EvalSymlinks(path)
		if err != nil {
			return fail("The server command could not be resolved.")
		}
		for _, arg := range c.Args {
			if len(arg) > 8192 || strings.ContainsRune(arg, '\x00') {
				return fail("Server arguments are too long or contain unsupported characters.")
			}
		}
		if c.Auth != "none" {
			return fail("Supply a local server's credentials as named environment values.")
		}
	} else {
		return fail("Choose a server URL or a local server command.")
	}
	if !slices.Contains([]string{"none", "token", "headers", "oauth"}, c.Auth) {
		return fail("Choose a supported sign-in method.")
	}
	if len(credentials.Token) > 16384 || hasControl(credentials.Token) {
		return fail("The access token must be a single line under 16 KiB.")
	}
	if c.Auth == "token" && credentials.Token == "" {
		return fail("Enter the access token supplied by the service.")
	}
	if len(credentials.Headers) > 24 || len(credentials.Env) > 64 {
		return fail("Use at most 24 headers or 64 environment values.")
	}
	for name, value := range credentials.Headers {
		lower := strings.ToLower(name)
		if !appHeaderName.MatchString(name) || hasControl(value) || len(value) > 16384 || strings.HasPrefix(lower, "mcp-") || slices.Contains([]string{"host", "content-length", "content-type", "connection", "transfer-encoding"}, lower) {
			return fail("Use ordinary sign-in headers; protocol and routing headers are managed by MCP.")
		}
	}
	for name, value := range credentials.Env {
		if !appEnvName.MatchString(name) || strings.ContainsRune(value, '\x00') || len(value) > 32768 || slices.Contains([]string{"PATH", "HOME", "LD_PRELOAD", "LD_LIBRARY_PATH", "OAUTH_HOME", "MCP_HEADERS"}, name) {
			return fail("Supply service-specific environment names and bounded values.")
		}
	}
	if c.Auth == "headers" && len(credentials.Headers) == 0 {
		return fail("Add the sign-in header required by this service.")
	}
	if c.Auth == "oauth" {
		if c.OAuthPath == "" {
			return fail("Install OAuth in the selected Bench suite to sign in through the browser.")
		}
		if c.ClientID == "" || len(c.ClientID) > 4096 || hasControl(c.ClientID) || len(c.Scopes) > 4096 || hasControl(c.Scopes) || len(credentials.ClientSecret) > 32768 {
			return fail("Enter the OAuth client ID supplied by this service; use a client secret only if the service requires one.")
		}
		if c.OAuthFlow == "" {
			c.OAuthFlow = "auto"
		}
		if !slices.Contains([]string{"auto", "code", "device", "client-credentials"}, c.OAuthFlow) {
			return fail("Choose browser, device, or machine sign-in.")
		}
	}
	// Do not retain credentials from an inactive form panel.
	if c.Auth != "token" {
		credentials.Token = ""
	}
	if c.Auth != "headers" {
		credentials.Headers = nil
	}
	if c.Auth != "oauth" {
		credentials.ClientSecret = ""
		c.ClientID = ""
		c.Scopes = ""
		c.OAuthFlow = ""
	}
	return c, credentials, nil
}

// Caller holds appsMu. Only public service fields leave the controller.
func (a *application) connectedAppView(c ConnectedApp) map[string]any {
	view := map[string]any{"id": c.ID, "name": c.Name, "transport": c.Transport, "url": c.URL, "command": c.Command, "args": c.Args, "protocol": c.Protocol, "auth": c.Auth, "clientId": c.ClientID, "scopes": c.Scopes, "oauthFlow": c.OAuthFlow, "allowPrivate": c.AllowPrivate, "enabled": c.Enabled, "state": c.State, "error": c.Error, "createdAt": c.CreatedAt, "checkedAt": c.CheckedAt}
	dir, _ := a.appDir(c.ID)
	var credentials AppCredentials
	if readJSON(filepath.Join(dir, "credentials.json"), &credentials) == nil {
		names := []string{}
		for key := range credentials.Env {
			names = append(names, key)
		}
		slices.Sort(names)
		view["environmentNames"] = names
		names = []string{}
		for key := range credentials.Headers {
			names = append(names, key)
		}
		slices.Sort(names)
		view["headerNames"] = names
		view["hasCredential"] = credentials.Token != "" || len(credentials.Headers) > 0 || len(credentials.Env) > 0 || c.Auth == "oauth"
	}
	if op := a.appOperations[c.ID]; op != nil {
		view["operation"] = op
	}
	if catalog, err := a.appCatalogue(c); err == nil {
		view["catalogue"] = catalog
	}
	return view
}

func (a *application) handleListConnectedApps(w http.ResponseWriter, r *http.Request) {
	a.appsMu.Lock()
	defer a.appsMu.Unlock()
	entries, err := os.ReadDir(filepath.Join(a.dataRoot, "connections", "services"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeError(w, 500, "connections", err.Error(), "")
		return
	}
	connections := []map[string]any{}
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		c, err := a.loadConnectedApp(entry.Name())
		if err != nil {
			continue
		}
		a.recoverAppOperation(&c)
		connections = append(connections, a.connectedAppView(c))
	}
	slices.SortFunc(connections, func(x, y map[string]any) int { return strings.Compare(x["name"].(string), y["name"].(string)) })
	writeJSON(w, 200, map[string]any{"connections": connections, "available": a.tools.path("mcp") != "" && a.tools.path("mcpbox") != "", "legacyAvailable": a.tools.path("mcp-legacy") != "", "oauthAvailable": a.tools.path("oauth") != ""})
}
func (a *application) handleGetConnectedApp(w http.ResponseWriter, r *http.Request) {
	a.appsMu.Lock()
	defer a.appsMu.Unlock()
	c, err := a.loadConnectedApp(r.PathValue("connection"))
	if err != nil {
		writeError(w, 404, "connections", "Connection not found.", "")
		return
	}
	a.recoverAppOperation(&c)
	writeJSON(w, 200, map[string]any{"connection": a.connectedAppView(c)})
}
func (a *application) recoverAppOperation(c *ConnectedApp) {
	if (c.State == "checking" || c.State == "signing-in") && a.appOperations[c.ID] == nil {
		c.State, c.Error = "error", "The connection check or sign-in was interrupted. Try again when you are ready."
		_ = a.saveConnectedApp(*c)
	}
}
func (a *application) handleCreateConnectedApp(w http.ResponseWriter, r *http.Request) {
	var in appConnectInput
	if err := decodeJSON(r, &in, 128<<10); err != nil {
		writeError(w, 400, "connections", err.Error(), "")
		return
	}
	c, credentials, err := a.validateAppInput(in)
	if err != nil {
		writeError(w, 400, "connections", err.Error(), "")
		return
	}
	c.ID = newRequestID("app", a.now())
	dir, err := a.appDir(c.ID)
	if err == nil {
		err = writeJSONAtomic(filepath.Join(dir, "credentials.json"), credentials, true)
	}
	if err == nil {
		err = writeJSONAtomic(filepath.Join(dir, "connection.json"), c, true)
	}
	if err == nil {
		err = a.writeAppTransport(c)
	}
	if err != nil {
		writeError(w, 500, "connections", err.Error(), "")
		return
	}
	a.appsMu.Lock()
	defer a.appsMu.Unlock()
	c, err = a.startAppOperation(c, c.Auth == "oauth")
	if err != nil {
		writeError(w, 500, "connections", err.Error(), "")
		return
	}
	writeJSON(w, 202, map[string]any{"connection": a.connectedAppView(c)})
}

func (a *application) handleConnectedAppAction(w http.ResponseWriter, r *http.Request) {
	a.appsMu.Lock()
	defer a.appsMu.Unlock()
	c, err := a.loadConnectedApp(r.PathValue("connection"))
	if err != nil {
		writeError(w, 404, "connections", "Connection not found.", "")
		return
	}
	action := r.PathValue("action")
	if action == "disconnect" {
		if op := a.appOperations[c.ID]; op != nil {
			op.cancel()
			delete(a.appOperations, c.ID)
		}
		c.Enabled, c.State, c.Error = false, "disconnected", ""
		if err := a.saveConnectedApp(c); err != nil {
			writeError(w, 500, "connections", err.Error(), "")
			return
		}
		writeJSON(w, 200, map[string]any{"connection": a.connectedAppView(c)})
		return
	}
	if !slices.Contains([]string{"check", "reconnect", "sign-in"}, action) {
		writeError(w, 404, "connections", "Unknown connection action.", "")
		return
	}
	if a.appOperations[c.ID] != nil {
		writeError(w, 409, "connections", "This connection is already being checked or signed in.", "")
		return
	}
	if action == "sign-in" && c.Auth != "oauth" {
		writeError(w, 400, "connections", "This service does not use browser sign-in.", "")
		return
	}
	c.Enabled = true
	c, err = a.startAppOperation(c, action == "sign-in")
	if err != nil {
		writeError(w, 500, "connections", err.Error(), "")
		return
	}
	writeJSON(w, 202, map[string]any{"connection": a.connectedAppView(c)})
}

func (a *application) startAppOperation(c ConnectedApp, login bool) (ConnectedApp, error) {
	if a.appOperations == nil {
		a.appOperations = map[string]*appOperation{}
	}
	ctx, cancel := context.WithTimeout(a.backgroundContext(), 6*time.Minute)
	op := &appOperation{cancel: cancel, ExpiresAt: time.Now().Add(6 * time.Minute)}
	a.appOperations[c.ID] = op
	c.State, c.Error = "checking", ""
	if login {
		c.State = "signing-in"
	}
	if err := a.saveConnectedApp(c); err != nil {
		cancel()
		delete(a.appOperations, c.ID)
		return c, err
	}
	go func() {
		defer cancel()
		var err error
		if login {
			err = a.loginConnectedApp(ctx, c, op)
		}
		var catalog AppCatalogue
		if err == nil {
			catalog, err = a.discoverConnectedApp(ctx, c)
		}
		a.appsMu.Lock()
		defer a.appsMu.Unlock()
		if a.appOperations[c.ID] != op {
			return
		}
		delete(a.appOperations, c.ID)
		current, loadErr := a.loadConnectedApp(c.ID)
		if loadErr != nil {
			return
		}
		if err != nil {
			current.State, current.Error = "error", a.redactAppError(c, err.Error())
		} else {
			now := a.now()
			current.State, current.Error, current.CatalogueID, current.CheckedAt = "ready", "", catalog.ID, &now
		}
		_ = a.saveConnectedApp(current)
	}()
	return c, nil
}

func (a *application) redactAppError(c ConnectedApp, message string) string {
	dir, _ := a.appDir(c.ID)
	var secret AppCredentials
	_ = readJSON(filepath.Join(dir, "credentials.json"), &secret)
	values := []string{secret.Token, secret.ClientSecret}
	for _, v := range secret.Headers {
		values = append(values, v)
	}
	for _, v := range secret.Env {
		values = append(values, v)
	}
	for _, v := range values {
		if v != "" {
			message = strings.ReplaceAll(message, v, "[credential]")
		}
	}
	if len(message) > 1800 {
		message = message[:1800] + "…"
	}
	return message
}

func (a *application) discoverConnectedApp(ctx context.Context, c ConnectedApp) (AppCatalogue, error) {
	catalog := AppCatalogue{ID: newRequestID("catalogue", a.now()), ConnectionID: c.ID, CreatedAt: a.now(), Capabilities: []AppCapability{}}
	dir, err := a.appDir(c.ID)
	if err != nil {
		return catalog, err
	}
	root := filepath.Join(dir, "catalogues", catalog.ID)
	box := filepath.Join(root, "box")
	args := []string{"make", "-mcp", filepath.Join(dir, "transport"), box, "--"}
	if c.Transport == "http" {
		args = append(args, c.URL)
	} else {
		args = append(args, c.Command)
		args = append(args, c.Args...)
	}
	out, errout, code, err := a.connectedCommand(ctx, "mcpbox", args, nil, 3*time.Minute)
	if err != nil || code != 0 {
		return catalog, fmt.Errorf("Could not connect: %s", connectedFailure(append(errout, out...), err))
	}
	discovery, err := readRegularFileLimit(filepath.Join(box, "discover.json"), 8<<20)
	if err != nil {
		return catalog, err
	}
	var info struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(discovery, &info); err != nil {
		return catalog, err
	}
	if raw := info.Meta["io.modelcontextprotocol/serverInfo"]; raw != nil {
		_ = json.Unmarshal(raw, &info.ServerInfo)
	}
	catalog.ServerName, catalog.ServerVersion = info.ServerInfo.Name, info.ServerInfo.Version
	for _, kind := range []string{"tools", "resources", "templates", "prompts"} {
		listed, stderr, code, err := a.connectedCommand(ctx, "mcpbox", []string{kind, box}, nil, 15*time.Second)
		if err != nil || code != 0 {
			return catalog, fmt.Errorf("Could not read %s: %s", kind, firstLine(stderr, err))
		}
		digests := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
			parts := strings.SplitN(line, "\t", 3)
			if len(parts) >= 2 {
				digests[parts[0]] = parts[1]
			}
		}
		raw, err := readRegularFileLimit(filepath.Join(box, "catalog", kind+".jsonl"), 8<<20)
		if err != nil {
			return catalog, err
		}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if line == "" {
				continue
			}
			var desc struct {
				Name        string `json:"name"`
				Title       string `json:"title"`
				Description string `json:"description"`
				URI         string `json:"uri"`
				Template    string `json:"uriTemplate"`
				Annotations struct {
					ReadOnly bool `json:"readOnlyHint"`
				} `json:"annotations"`
			}
			if err := json.Unmarshal([]byte(line), &desc); err != nil {
				return catalog, err
			}
			name := desc.Name
			if kind == "resources" {
				name = desc.URI
			}
			if kind == "templates" {
				name = desc.Template
			}
			digest := digests[name]
			if name == "" || len(name) > 2048 || hasControl(name) || len(digest) != 64 || len(line) > 256<<10 || len(catalog.Capabilities) >= 1024 {
				return catalog, errors.New("This service catalogue has unsupported names or exceeds the review limits (1,024 capabilities, 256 KiB per description).")
			}
			title := desc.Title
			if title == "" {
				title = desc.Name
			}
			if title == "" {
				title = name
			}
			catalog.Capabilities = append(catalog.Capabilities, AppCapability{Kind: kind, Name: name, Title: title, Description: desc.Description, Digest: digest, Descriptor: json.RawMessage(line), ReadOnlyHint: desc.Annotations.ReadOnly})
		}
	}
	catalog.SHA256 = catalogueDigest(catalog)
	if err := writeJSONAtomic(filepath.Join(root, "catalogue.json"), catalog, true); err != nil {
		return catalog, err
	}
	return catalog, nil
}

func connectedFailure(raw []byte, err error) string {
	message := strings.TrimSpace(string(raw))
	if message == "" {
		if err != nil {
			return err.Error()
		}
		return "The service returned no diagnostic."
	}
	lines := strings.Split(message, "\n")
	message = strings.Join(lines[:min(len(lines), 2)], " ")
	if len(message) > 1200 {
		message = message[:1200] + "…"
	}
	return message
}
