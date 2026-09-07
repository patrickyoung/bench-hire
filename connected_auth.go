package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

type appBoundedOutput struct {
	mu       sync.Mutex
	data     []byte
	limit    int
	overflow bool
}

func (o *appBoundedOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := min(len(p), max(0, o.limit-len(o.data)))
	o.data = append(o.data, p[:n]...)
	o.overflow = o.overflow || n < len(p)
	return len(p), nil
}
func (o *appBoundedOutput) bytes() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.data)
}

// The public Bench commands retain ownership of protocols. This wrapper only
// bounds diagnostics and lifetime, preserving literal argv and exact stdout.
func runConnectedCommand(ctx context.Context, path string, args []string, dir string, input []byte, env []string, timeout time.Duration) ([]byte, []byte, int, error) {
	return runConnectedCommandMode(ctx, path, args, dir, input, env, timeout, true)
}

func runConnectedCommandMode(ctx context.Context, path string, args []string, dir string, input []byte, env []string, timeout time.Duration, ownGroup bool) ([]byte, []byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(input)
	stdout, stderr := &appBoundedOutput{limit: 8 << 20}, &appBoundedOutput{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if ownGroup {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	cmd.Cancel = func() error {
		if !ownGroup {
			return cmd.Process.Signal(syscall.SIGTERM)
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 3 * time.Second
	err := cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
			err = nil
		} else {
			code = -1
		}
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if stdout.overflow {
		err = errors.New("service output exceeds 8 MiB")
	}
	if stderr.overflow {
		err = errors.New("service diagnostics exceed 64 KiB")
	}
	return stdout.bytes(), stderr.bytes(), code, err
}
func (a *application) connectedCommand(ctx context.Context, name string, args []string, input []byte, timeout time.Duration) ([]byte, []byte, int, error) {
	env := append(os.Environ(), a.tools.agentEnvironment()...)
	return runConnectedCommand(ctx, a.tools.path(name), args, a.dataRoot, input, env, timeout)
}
func (a *application) writeAppTransport(c ConnectedApp) error {
	dir, err := a.appDir(c.ID)
	if err != nil {
		return err
	}
	script := "#!/bin/sh\nexec " + shellQuote(a.executable) + " mcp-transport " + shellQuote(filepath.Join(dir, "connection.json")) + " \"$@\"\n"
	path := filepath.Join(dir, "transport")
	if err := writeFileAtomic(path, []byte(script), false); err != nil {
		return err
	}
	return os.Chmod(path, 0700)
}
func connectedEnvironment(c ConnectedApp, secret AppCredentials, dir string) []string {
	env := []string{}
	for _, key := range []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "USER", "LOGNAME", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	env = append(env, "OAUTH_HOME="+filepath.Join(dir, "oauth"))
	for key, value := range secret.Env {
		env = append(env, key+"="+value)
	}
	return env
}

// Generated MCPbox programs invoke this shim outside the employee's Cage. It
// binds credentials to the configured endpoint and delegates to MCP/OAuth.
func runMCPGrantTransport(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 5 {
		fmt.Fprintln(stderr, "invalid employee app transport")
		return 2
	}
	var connection ConnectedApp
	var worker Worker
	if readJSON(args[1], &connection) != nil || readJSON(args[3], &worker) != nil {
		fmt.Fprintln(stderr, "cannot verify employee app access")
		return 2
	}
	grant, err := readAppGrant(args[0], connection.ID)
	if err != nil || !grant.Enabled || !connection.Enabled || grant.Version != args[2] || grant.WorkerSlug != worker.Slug || worker.RetiredAt != nil || worker.RetiringAt != nil {
		fmt.Fprintln(stderr, "employee app access changed or was revoked")
		return 2
	}
	return runMCPTransport(append([]string{args[1]}, args[4:]...), stdin, stdout, stderr)
}

func runMCPTransport(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(stderr, "connection:", err); return 2 }
	if len(args) < 3 {
		return fail(errors.New("missing service configuration or MCP operation"))
	}
	configPath := args[0]
	var c ConnectedApp
	if err := readJSON(configPath, &c); err != nil {
		return fail(errors.New("service configuration is unavailable"))
	}
	if !c.Enabled {
		return fail(errors.New("this service is disconnected"))
	}
	dir := filepath.Dir(configPath)
	var secret AppCredentials
	if err := readJSON(filepath.Join(dir, "credentials.json"), &secret); err != nil {
		return fail(errors.New("service sign-in details are unavailable"))
	}
	callArgs := slices.Clone(args[1:])
	split := slices.Index(callArgs, "--")
	if split < 1 {
		return fail(errors.New("MCP endpoint is missing"))
	}
	expected := []string{c.URL}
	if c.Transport == "stdio" {
		expected = append([]string{c.Command}, c.Args...)
	}
	if !slices.Equal(callArgs[split+1:], expected) {
		return fail(errors.New("the reviewed service endpoint changed; reconnect and review access"))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	command := c.MCPPath
	var headerFile *os.File
	// Secret headers travel on a descriptor. They never enter argv, endpoint
	// metadata, capability descriptors, or the worker's environment.
	if c.Auth == "token" || c.Auth == "headers" {
		headers := map[string]string{}
		if c.Auth == "token" {
			headers["Authorization"] = "Bearer " + secret.Token
		} else {
			headers = secret.Headers
		}
		var content strings.Builder
		keys := []string{}
		for key := range headers {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			if !appHeaderName.MatchString(key) || hasControl(headers[key]) {
				return fail(errors.New("invalid sign-in header"))
			}
			fmt.Fprintf(&content, "%s: %s\n", key, headers[key])
		}
		f, err := os.CreateTemp(dir, ".headers-")
		if err != nil {
			return fail(err)
		}
		defer f.Close()
		defer os.Remove(f.Name())
		if _, err := io.WriteString(f, content.String()); err != nil {
			return fail(err)
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return fail(err)
		}
		// Keep the open descriptor, but no named copy beyond credentials.json.
		if err := os.Remove(f.Name()); err != nil {
			return fail(err)
		}
		headerFile = f
		callArgs = append([]string{callArgs[0], "-header-fd", "3"}, callArgs[1:]...)
	} else if c.Auth == "oauth" {
		if c.OAuthPath == "" {
			return fail(errors.New("OAuth is not installed"))
		}
		callArgs = append([]string{callArgs[0], "-header-fd", "3"}, callArgs[1:]...)
		oauthArgs := []string{"with"}
		if c.AllowPrivate {
			oauthArgs = append(oauthArgs, "-allow-private")
		}
		oauthArgs = append(oauthArgs, c.ID, "--", c.MCPPath)
		callArgs = append(oauthArgs, callArgs...)
		command = c.OAuthPath
	}
	cmd := exec.CommandContext(ctx, command, callArgs...)
	cmd.Env = connectedEnvironment(c, secret, dir)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if headerFile != nil {
		cmd.ExtraFiles = []*os.File{headerFile}
	}
	// The shim and child share the controller's process group. Native MCP owns
	// server process cleanup, and receives the same cancellation signal.
	cmd.WaitDelay = 3 * time.Second
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 {
		return exit.ExitCode()
	}
	return fail(err)
}

type appLoginOutput struct {
	app  *application
	id   string
	op   *appOperation
	mu   sync.Mutex
	line string
	log  appBoundedOutput
}

func (o *appLoginOutput) Write(p []byte) (int, error) {
	_, _ = o.log.Write(p)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.line += string(p)
	for {
		line, rest, ok := strings.Cut(o.line, "\n")
		if !ok {
			break
		}
		o.line = rest
		value, code := strings.TrimSpace(line), ""
		if strings.HasPrefix(value, "oauth: open ") && strings.Contains(value, " and enter code ") {
			value, code, _ = strings.Cut(strings.TrimPrefix(value, "oauth: open "), " and enter code ")
		}
		u, err := url.Parse(value)
		if err != nil || !slices.Contains([]string{"http", "https"}, u.Scheme) || u.Host == "" || u.User != nil {
			continue
		}
		o.app.appsMu.Lock()
		if o.app.appOperations[o.id] == o.op {
			o.op.LoginURL, o.op.UserCode = value, code
		}
		o.app.appsMu.Unlock()
	}
	if len(o.line) > 8192 {
		o.line = ""
	}
	return len(p), nil
}
func (a *application) loginConnectedApp(ctx context.Context, c ConnectedApp, op *appOperation) error {
	dir, err := a.appDir(c.ID)
	if err != nil {
		return err
	}
	var secret AppCredentials
	if err := readJSON(filepath.Join(dir, "credentials.json"), &secret); err != nil {
		return err
	}
	args := []string{"login", c.ID, "-no-browser", "-replace", "-timeout", "5m", "-flow", c.OAuthFlow, "-client-id", c.ClientID}
	if c.AllowPrivate {
		args = append(args, "-allow-private")
	}
	if c.Scopes != "" {
		args = append(args, "-scope", c.Scopes)
	}
	if secret.ClientSecret != "" {
		args = append(args, "-client-secret-stdin")
	}
	args = append(args, c.URL)
	cmd := exec.CommandContext(ctx, c.OAuthPath, args...)
	cmd.Env = connectedEnvironment(c, AppCredentials{}, dir)
	cmd.Stdin = strings.NewReader(secret.ClientSecret)
	output := &appLoginOutput{app: a, id: c.ID, op: op, log: appBoundedOutput{limit: 64 << 10}}
	cmd.Stdout, cmd.Stderr = output, output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 3 * time.Second
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	if err := cmd.Run(); err != nil {
		// The authorization URL is useful only while login is in progress. It is
		// never persisted as a diagnostic or reused after expiry.
		message := err.Error()
		for _, line := range strings.Split(string(output.log.bytes()), "\n") {
			if strings.HasPrefix(line, "oauth:") && !strings.HasPrefix(line, "oauth: open") {
				message = line
			}
		}
		return fmt.Errorf("Sign-in did not finish: %s", message)
	}
	return nil
}

func (a *application) handleUpdateConnectedAuth(w http.ResponseWriter, r *http.Request) {
	var in struct {
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
	if err := decodeJSON(r, &in, 128<<10); err != nil {
		writeError(w, 400, "connections", err.Error(), "")
		return
	}
	a.appsMu.Lock()
	defer a.appsMu.Unlock()
	c, err := a.loadConnectedApp(r.PathValue("connection"))
	if err != nil {
		writeError(w, 404, "connections", "Connection not found.", "")
		return
	}
	if a.appOperations[c.ID] != nil {
		writeError(w, 409, "connections", "Wait for the current connection check or sign-in to finish.", "")
		return
	}
	updated, secret, err := a.validateAppInput(appConnectInput{Name: c.Name, Transport: c.Transport, URL: c.URL, Command: c.Command, Args: c.Args, Protocol: c.Protocol, Auth: in.Auth, Token: in.Token, Headers: in.Headers, Env: in.Env, ClientID: in.ClientID, ClientSecret: in.ClientSecret, Scopes: in.Scopes, OAuthFlow: in.OAuthFlow, AllowPrivate: in.AllowPrivate})
	if err != nil {
		writeError(w, 400, "connections", err.Error(), "")
		return
	}
	updated.ID, updated.CreatedAt, updated.CatalogueID = c.ID, c.CreatedAt, c.CatalogueID
	dir, _ := a.appDir(c.ID)
	if err := writeJSONAtomic(filepath.Join(dir, "credentials.json"), secret, false); err != nil {
		writeError(w, 500, "connections", err.Error(), "")
		return
	}
	if err := a.saveConnectedApp(updated); err != nil {
		writeError(w, 500, "connections", err.Error(), "")
		return
	}
	updated, err = a.startAppOperation(updated, updated.Auth == "oauth")
	if err != nil {
		writeError(w, 500, "connections", err.Error(), "")
		return
	}
	writeJSON(w, 202, map[string]any{"connection": a.connectedAppView(updated)})
}
