package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// The openai-codex provider authenticates with the ChatGPT login that the
// official Codex CLI keeps in ~/.codex/auth.json. Hire never logs in: it reads
// that file the way the pre-0.2 ask did, refreshes a stale access token into
// its own private copy, and hands the header to ask on descriptor 3 only.
const (
	codexClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexTokenURL     = "https://auth.openai.com/oauth/token"
	codexRefreshAhead = 5 * time.Minute
)

var askSubcommands = map[string]bool{"replay": true, "compact": true, "note": true, "append": true, "context": true, "system": true, "version": true, "help": true}

type codexTokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
	RefreshedAt  time.Time `json:"refreshed_at"`
	Source       string    `json:"source"`
}

type codexStatus struct {
	Available bool      `json:"available"`
	Path      string    `json:"path"`
	Source    string    `json:"source,omitempty"`
	Expiry    time.Time `json:"expiry"`
	AccountID string    `json:"accountId,omitempty"`
	Message   string    `json:"message"`
}

func codexAuthPath() string {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return filepath.Join(home, "auth.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "auth.json")
}

// readCodexCLI converts the Codex CLI file to Hire's narrow token shape.
func readCodexCLI(path string) (codexTokens, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return codexTokens{}, err
	}
	var f struct {
		AuthMode string `json:"auth_mode"`
		Tokens   struct {
			IDToken      string `json:"id_token"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			AccountID    string `json:"account_id"`
		} `json:"tokens"`
		LastRefresh time.Time `json:"last_refresh"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return codexTokens{}, fmt.Errorf("%s: %w", path, err)
	}
	if f.AuthMode != "chatgpt" {
		return codexTokens{}, fmt.Errorf("%s has auth mode %q, want chatgpt; run codex login", path, f.AuthMode)
	}
	if f.Tokens.AccessToken == "" && f.Tokens.RefreshToken == "" {
		return codexTokens{}, fmt.Errorf("%s holds no ChatGPT tokens; run codex login", path)
	}
	t := codexTokens{AccessToken: f.Tokens.AccessToken, RefreshToken: f.Tokens.RefreshToken, IDToken: f.Tokens.IDToken, AccountID: f.Tokens.AccountID, RefreshedAt: f.LastRefresh, Source: "codex-cli"}
	if t.AccountID == "" {
		t.AccountID = jwtAccountID(t.IDToken)
	}
	return t, nil
}

func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}

func jwtExpiry(token string) time.Time {
	claims := jwtClaims(token)
	exp, ok := claims["exp"].(float64)
	if !ok || exp <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(exp), 0).UTC()
}

func jwtAccountID(idToken string) string {
	claims := jwtClaims(idToken)
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	id, _ := auth["chatgpt_account_id"].(string)
	return id
}

// loadCodexTokens prefers whichever copy expires later: the Codex CLI's own
// file (refreshed whenever the person uses codex) or Hire's private copy.
func loadCodexTokens(cachePath string) (codexTokens, string, error) {
	cliPath := codexAuthPath()
	cli, cliErr := readCodexCLI(cliPath)
	var cache codexTokens
	cacheErr := errors.New("no cache")
	if cachePath != "" {
		cacheErr = readJSON(cachePath, &cache)
	}
	switch {
	case cliErr != nil && cacheErr != nil:
		return codexTokens{}, cliPath, cliErr
	case cliErr != nil:
		return cache, cachePath, nil
	case cacheErr != nil:
		return cli, cliPath, nil
	}
	if jwtExpiry(cache.AccessToken).After(jwtExpiry(cli.AccessToken)) {
		return cache, cachePath, nil
	}
	return cli, cliPath, nil
}

func inspectCodex(cachePath string) codexStatus {
	tokens, path, err := loadCodexTokens(cachePath)
	if err != nil {
		return codexStatus{Path: path, Message: "No Codex CLI login was found at " + path + " (" + err.Error() + ")."}
	}
	expiry := jwtExpiry(tokens.AccessToken)
	status := codexStatus{Available: true, Path: path, Source: tokens.Source, Expiry: expiry, AccountID: tokens.AccountID}
	switch {
	case tokens.RefreshToken == "" && !expiry.After(time.Now()):
		status.Available = false
		status.Message = "The Codex login at " + path + " has expired and has no refresh token; run codex login."
	case expiry.IsZero():
		status.Message = "Codex login found at " + path + "; the access token carries no expiry, so Hire will refresh it on first use."
	default:
		status.Message = "Codex login found at " + path + "; access token valid until " + expiry.Local().Format("Jan 2 15:04") + ", refreshed automatically after that."
	}
	return status
}

func codexTokenEndpoint() string {
	if v := os.Getenv("HIRE_CODEX_TOKEN_URL"); v != "" {
		return v
	}
	return codexTokenURL
}

// refreshCodex is the RFC 6749 refresh_token grant, form-encoded, the same
// request the earlier ask sent. The client never follows a redirect with the
// refresh token in its body.
func refreshCodex(ctx context.Context, t codexTokens) (codexTokens, error) {
	if t.RefreshToken == "" {
		return t, errors.New("no refresh token; run codex login")
	}
	endpoint := codexTokenEndpoint()
	if u, err := url.Parse(endpoint); err != nil || (u.Scheme != "https" && u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		return t, fmt.Errorf("refusing to send a refresh token to %q", endpoint)
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.RefreshToken)
	form.Set("client_id", envOr("HIRE_CODEX_CLIENT_ID", codexClientID))
	form.Set("scope", "openid profile email")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return t, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("token endpoint redirected") }}
	resp, err := client.Do(req)
	if err != nil {
		return t, fmt.Errorf("codex token refresh: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return t, fmt.Errorf("codex token refresh: HTTP %d: %s", resp.StatusCode, firstLineText(string(body)))
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
		return t, errors.New("codex token refresh returned no access_token")
	}
	next := codexTokens{AccessToken: tok.AccessToken, RefreshToken: t.RefreshToken, IDToken: t.IDToken, AccountID: t.AccountID, RefreshedAt: time.Now(), Source: "hire-refresh"}
	if tok.RefreshToken != "" {
		next.RefreshToken = tok.RefreshToken
	}
	if tok.IDToken != "" {
		next.IDToken = tok.IDToken
		if id := jwtAccountID(tok.IDToken); id != "" {
			next.AccountID = id
		}
	}
	return next, nil
}

// codexAuthorization returns a header good for at least codexRefreshAhead,
// refreshing under a file lock so parallel ask calls refresh once.
func codexAuthorization(ctx context.Context, cachePath string) (string, string, error) {
	tokens, _, err := loadCodexTokens(cachePath)
	if err != nil {
		return "", "", err
	}
	expiry := jwtExpiry(tokens.AccessToken)
	if tokens.AccessToken != "" && !expiry.IsZero() && expiry.After(time.Now().Add(codexRefreshAhead)) {
		return "Authorization: Bearer " + tokens.AccessToken, tokens.AccountID, nil
	}
	if cachePath == "" {
		return "", "", errors.New("the Codex access token has expired and Hire has nowhere to keep a refreshed one")
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		return "", "", err
	}
	lock, err := os.OpenFile(cachePath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", "", err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return "", "", err
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	// Another process may have refreshed while this one waited.
	if again, _, err := loadCodexTokens(cachePath); err == nil {
		if exp := jwtExpiry(again.AccessToken); again.AccessToken != "" && !exp.IsZero() && exp.After(time.Now().Add(codexRefreshAhead)) {
			return "Authorization: Bearer " + again.AccessToken, again.AccountID, nil
		}
		tokens = again
	}
	refreshed, err := refreshCodex(ctx, tokens)
	if err != nil {
		return "", "", err
	}
	if err := writeJSONAtomic(cachePath, refreshed, false); err != nil {
		return "", "", err
	}
	return "Authorization: Bearer " + refreshed.AccessToken, refreshed.AccountID, nil
}

// modelFromArgs finds ask's -m value the way Go's flag package would read it.
func modelFromArgs(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-m" || arg == "--m":
			if i+1 < len(args) {
				return args[i+1]
			}
		case strings.HasPrefix(arg, "-m="):
			return strings.TrimPrefix(arg, "-m=")
		case strings.HasPrefix(arg, "--m="):
			return strings.TrimPrefix(arg, "--m=")
		}
	}
	return os.Getenv("ASK_MODEL")
}

// headerArgs inserts -header-fd 3 where ask's flag parser will see it, or
// reports that this invocation makes no model call and needs no header.
func headerArgs(args []string) ([]string, bool) {
	if len(args) > 0 && askSubcommands[args[0]] {
		if args[0] != "compact" {
			return args, false
		}
		return append([]string{"compact", "-header-fd", "3"}, args[1:]...), true
	}
	return append([]string{"-header-fd", "3"}, args...), true
}

// runAskShim is `hire ask ARGS`: the executable Hire installs as AGENT_ASK.
// For any provider except openai-codex it is an exact pass-through.
func runAskShim(args []string, stderr io.Writer) int {
	real := os.Getenv("HIRE_REAL_ASK")
	if real == "" {
		found, err := exec.LookPath("ask")
		if err != nil {
			fmt.Fprintln(stderr, "hire ask: ask is not on PATH and HIRE_REAL_ASK is unset")
			return 1
		}
		real = found
	}
	env := os.Environ()
	provider, _, _ := strings.Cut(modelFromArgs(args), "/")
	if provider != "openai-codex" {
		return execProgram(real, append([]string{real}, args...), env, stderr)
	}
	withHeader, needed := headerArgs(args)
	if !needed {
		return execProgram(real, append([]string{real}, args...), env, stderr)
	}
	if profile := os.Getenv("HIRE_OAUTH_PROFILE"); profile != "" {
		oauth, err := exec.LookPath("oauth")
		if err != nil {
			fmt.Fprintln(stderr, "hire ask: HIRE_OAUTH_PROFILE is set but oauth is not on PATH")
			return 1
		}
		argv := append([]string{oauth, "with", profile, "--", real}, withHeader...)
		return execProgram(oauth, argv, env, stderr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	header, accountID, err := codexAuthorization(ctx, os.Getenv("HIRE_CODEX_CACHE"))
	if err != nil {
		fmt.Fprintln(stderr, "hire ask: "+err.Error())
		return 1
	}
	if accountID != "" && os.Getenv("OPENAI_CODEX_ACCOUNT_ID") == "" {
		env = append(env, "OPENAI_CODEX_ACCOUNT_ID="+accountID)
	}
	r, w, err := os.Pipe()
	if err != nil {
		fmt.Fprintln(stderr, "hire ask:", err)
		return 1
	}
	if _, err := io.WriteString(w, header+"\n"); err != nil {
		fmt.Fprintln(stderr, "hire ask:", err)
		return 1
	}
	_ = w.Close()
	// ExtraFiles[0] is descriptor 3 in the child by contract, so the header
	// arrives without disturbing any descriptor this process holds.
	cmd := exec.Command(real, withHeader...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = env
	cmd.ExtraFiles = []*os.File{r}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(stderr, "hire ask:", err)
		return 1
	}
	_ = r.Close()
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for s := range signals {
			_ = cmd.Process.Signal(s)
		}
	}()
	err = cmd.Wait()
	signal.Stop(signals)
	close(signals)
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
		return exitErr.ExitCode()
	}
	fmt.Fprintln(stderr, "hire ask:", err)
	return 1
}

// execProgram replaces this process so signals, exit status, and stdio belong
// to ask directly; ply sees exactly one child.
func execProgram(path string, argv []string, env []string, stderr io.Writer) int {
	err := syscall.Exec(path, argv, env)
	fmt.Fprintf(stderr, "hire ask: exec %s: %v\n", path, err)
	return 126
}

// askShimScript is what Hire writes to var/bin/ask. The shim carries its own
// configuration so Tend's scrubbed environment cannot lose it.
func askShimScript(hire, realAsk, cache, profile string) string {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	return "#!/bin/sh\n# Bench Hire: run ask, adding the Codex OAuth header on descriptor 3 when the model needs it.\n" +
		"HIRE_REAL_ASK=" + q(realAsk) + " HIRE_CODEX_CACHE=" + q(cache) + " HIRE_OAUTH_PROFILE=" + q(profile) + " exec " + q(hire) + " ask \"$@\"\n"
}

// Hire checks the name's shape. The selected Ask executable owns provider,
// model, authentication and structured-output support; a second catalogue here
// would reject new Ask providers and misjudge gateways.
func validateModel(model string) error {
	model = strings.TrimSpace(model)
	provider, name, ok := strings.Cut(model, "/")
	if !ok || strings.TrimSpace(name) == "" || provider == "" {
		return fmt.Errorf("a model is provider/model, for example openai-codex/gpt-5.6-sol")
	}
	if provider == "codex" || provider == "chatgpt" {
		return fmt.Errorf("%q is not a provider ask knows; use openai-codex/%s", provider, name)
	}
	if len(model) > 512 || strings.IndexFunc(model, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 || strings.HasPrefix(provider, "-") || strings.HasPrefix(name, "-") {
		return fmt.Errorf("use a provider/model name without spaces or control characters")
	}
	return nil
}
