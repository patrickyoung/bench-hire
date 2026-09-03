package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed web/*
var webAssets embed.FS

const version = "0.1.0"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "exec":
			os.Exit(runExec(os.Args[2:], os.Stdout, os.Stderr))
		case "ask":
			os.Exit(runAskShim(os.Args[2:], os.Stderr))
		case "version", "-V", "--version":
			fmt.Println("hire " + version)
			return
		case "help", "-h", "--help":
			fmt.Print(usage)
			return
		default:
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
	}
	if err := serve(); err != nil {
		log.Fatal(err)
	}
}

const usage = `hire — create and deploy a digital worker on the Bench tools

  hire                        serve the local web app (HIRE_ADDR, default 127.0.0.1:8790)
  hire exec HOME REQUEST.json run one request through agent; this is what tend runs
  hire ask [ask args...]      run ask, adding the Codex OAuth header on descriptor 3
                              for openai-codex models; installed as AGENT_ASK
  hire version                print the version
  hire help                   print this summary

env: HIRE_ADDR HIRE_DATA HIRE_BIN_DIR HIRE_MODEL HIRE_WORKERS HIRE_JOB_MAX
     HIRE_OAUTH_PROFILE names an oauth profile to use instead of the Codex CLI login
     HIRE_JOBS=memory keeps jobs in memory (development only)
`

func serve() error {
	addr := envOr("HIRE_ADDR", "127.0.0.1:8790")
	if !isLoopback(addr) {
		return errors.New("HIRE_ADDR must bind to loopback; Hire is a single-user local controller")
	}
	dataRoot, err := filepath.Abs(envOr("HIRE_DATA", "./var"))
	if err != nil {
		return err
	}
	for _, sub := range []string{"hire", "workers", "tend", "ask"} {
		if err := os.MkdirAll(filepath.Join(dataRoot, sub), 0o700); err != nil {
			return fmt.Errorf("create data root: %w", err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	tools := newToolset(os.Getenv("HIRE_BIN_DIR"))
	if agentPath := tools.path("agent"); agentPath != "" {
		_ = os.Setenv("HIRE_AGENT", agentPath)
	}
	codexCache := filepath.Join(dataRoot, "hire", "codex-token.json")
	if realAsk := tools.path("ask"); realAsk != "" {
		shim := filepath.Join(dataRoot, "bin", "ask")
		if err := writeFileAtomic(shim, []byte(askShimScript(executable, realAsk, codexCache, os.Getenv("HIRE_OAUTH_PROFILE"))), false); err != nil {
			return fmt.Errorf("install ask wrapper: %w", err)
		}
		if err := os.Chmod(shim, 0o700); err != nil {
			return err
		}
		tools.paths["ask"] = shim
		tools.realAsk = realAsk
		_ = os.Setenv("AGENT_ASK", shim)
	}
	store, err := newStore(filepath.Join(dataRoot, "hire"))
	if err != nil {
		return err
	}
	var jobs Jobs
	if os.Getenv("HIRE_JOBS") == "memory" {
		log.Print("hire: HIRE_JOBS=memory — jobs are not durable across restarts")
		jobs = newMemoryJobs(filepath.Join(dataRoot, "tend"), time.Now)
	} else {
		jobs, err = newTendJobs(tools.path("tend"), filepath.Join(dataRoot, "tend"))
		if err != nil {
			return err
		}
	}
	assets, err := fs.Sub(webAssets, "web")
	if err != nil {
		return err
	}
	workers, _ := strconv.Atoi(envOr("HIRE_WORKERS", "2"))
	app := &application{
		store:       store,
		jobs:        jobs,
		tools:       tools,
		assets:      assets,
		dataRoot:    dataRoot,
		workersRoot: filepath.Join(dataRoot, "workers"),
		askDir:      filepath.Join(dataRoot, "ask"),
		executable:  executable,
		codexCache:  codexCache,
		location:    time.Local,
		now:         time.Now,
		model:       strings.TrimSpace(envOr("HIRE_MODEL", os.Getenv("ASK_MODEL"))),
		token:       randomHex(16),
		host:        addr,
	}
	app.runner = newRunner(app, workers)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app.runner.Start(ctx)
	server := &http.Server{
		Addr:              addr,
		Handler:           app.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       60 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	result := make(chan error, 1)
	log.Printf("Bench Hire %s listening on http://%s (data %s)", version, addr, dataRoot)
	go func() { result <- server.ListenAndServe() }()
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
		return nil
	}
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
