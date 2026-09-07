package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type appExecution struct {
	PID       int    `json:"pid"`
	Start     string `json:"start"`
	Home      string `json:"home"`
	RequestID string `json:"requestId"`
}

func recordAppExecution(workerDir, home, requestID string) (func(), error) {
	// No connection means no platform restriction on an ordinary Agent run.
	entries, err := os.ReadDir(filepath.Join(home, ".agent", "connections"))
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(entries) == 0) {
		return func() {}, nil
	}
	if err != nil {
		return nil, err
	}
	_, start, err := appProcessIdentity(os.Getpid())
	if err != nil {
		return nil, err
	}
	path := filepath.Join(workerDir, "app-execution.json")
	if err := writeJSONAtomic(path, appExecution{os.Getpid(), start, home, requestID}, false); err != nil {
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
}

type appBrokerLocation struct {
	Socket string `json:"socket"`
	PID    int    `json:"pid"`
	Start  string `json:"start"`
}
type appCallRequest struct {
	Home         string          `json:"home"`
	ConnectionID string          `json:"connectionId"`
	Operation    string          `json:"operation"`
	Name         string          `json:"name"`
	URI          string          `json:"uri,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
}
type appCallResponse struct {
	Exit  int      `json:"exit"`
	Error string   `json:"error,omitempty"`
	Call  *AppCall `json:"call,omitempty"`
	Data  any      `json:"data,omitempty"`
}

func (a *application) startAppBroker(ctx context.Context) (func(), error) {
	dir, err := os.MkdirTemp("", "hire-apps-")
	if err != nil {
		return nil, err
	}
	socket := filepath.Join(dir, "socket")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	location := filepath.Join(a.dataRoot, "connections", "broker.json")
	_, start, _ := appProcessIdentity(os.Getpid())
	if err := writeJSONAtomic(location, appBrokerLocation{Socket: socket, PID: os.Getpid(), Start: start}, false); err != nil {
		_ = listener.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	brokerCtx, cancel := context.WithCancel(ctx)
	go func() { <-brokerCtx.Done(); _ = listener.Close() }()
	go func() {
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			go a.serveAppCall(brokerCtx, conn)
		}
	}()
	return func() { cancel(); _ = listener.Close(); _ = os.RemoveAll(dir) }, nil
}

func (a *application) serveAppCall(ctx context.Context, conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Minute))
	pid, err := appPeerPID(conn)
	if err != nil {
		_ = json.NewEncoder(conn).Encode(appCallResponse{Exit: 2, Error: err.Error()})
		return
	}
	var in appCallRequest
	d := json.NewDecoder(io.LimitReader(conn, 32<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		_ = json.NewEncoder(conn).Encode(appCallResponse{Exit: 2, Error: "Invalid connected-app request"})
		return
	}
	response := a.callAppForPeer(ctx, pid, in)
	if response.Call != nil {
		public := a.publicAppCall(*response.Call)
		response.Call = &public
	}
	_ = json.NewEncoder(conn).Encode(response)
}

func (a *application) callAppForPeer(ctx context.Context, pid int, in appCallRequest) appCallResponse {
	fail := func(err error) appCallResponse { return appCallResponse{Exit: 2, Error: err.Error()} }
	home, err := filepath.Abs(in.Home)
	if err != nil {
		return fail(err)
	}
	grant, err := readAppGrant(home, in.ConnectionID)
	if err != nil {
		return fail(errors.New("This employee has no valid access to that app"))
	}
	worker, err := a.store.Worker(grant.WorkerSlug)
	if err != nil {
		return fail(err)
	}
	// Specialists have separate homes and permissions; a parent grant cannot
	// be used from a specialist or another employee's process.
	if home != a.homeDir(worker.Slug) {
		return fail(errors.New("The app grant does not belong to this home"))
	}
	var execution appExecution
	if err := readJSON(filepath.Join(a.store.workerDir(worker.Slug), "app-execution.json"), &execution); err != nil || execution.Home != home || !validID(execution.RequestID) || !appExecutionContains(pid, execution) {
		return fail(errors.New("App calls must come from this employee's active Hire task"))
	}
	return a.executeAppCall(ctx, worker, execution.RequestID, in)
}

func runAppCall(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 4 {
		fmt.Fprintln(stderr, "usage: hire app-call BROKER.json HOME CONNECTION list | describe KIND NAME | run TOOL | read URI | read-template TEMPLATE URI\nrun and read-template accept a JSON object on stdin. Calls retain receipts; an uncertain call must be reviewed before further use.")
		return 2
	}
	in := appCallRequest{Home: args[1], ConnectionID: args[2], Operation: args[3]}
	switch in.Operation {
	case "list":
		if len(args) != 4 {
			return 2
		}
	case "describe":
		if len(args) != 6 {
			return 2
		}
		in.Name = args[4] + "\x00" + args[5]
	case "run", "read":
		if len(args) != 5 {
			return 2
		}
		in.Name = args[4]
	case "read-template":
		if len(args) != 6 {
			return 2
		}
		in.Name, in.URI = args[4], args[5]
	default:
		fmt.Fprintln(stderr, "Unknown app operation. Use list, describe, run, read, or read-template.")
		return 2
	}
	if in.Operation == "run" || in.Operation == "read-template" {
		raw, err := io.ReadAll(io.LimitReader(stdin, (8<<10)+1))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		in.Input, err = canonicalAppJSON(raw)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	var location appBrokerLocation
	if err := readJSON(args[0], &location); err != nil {
		fmt.Fprintln(stderr, "Open Hire to use connected apps.")
		return 2
	}
	conn, err := net.DialTimeout("unix", location.Socket, 5*time.Second)
	if err != nil {
		fmt.Fprintln(stderr, "Hire's app connection is unavailable. Open Hire and inspect the task before trying again.")
		return 2
	}
	defer conn.Close()
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		fmt.Fprintln(stderr, "Cannot verify Hire's app controller.")
		return 2
	}
	peer, peerErr := appPeerPID(unix)
	_, start, identityErr := appProcessIdentity(peer)
	if peerErr != nil || identityErr != nil || peer != location.PID || start != location.Start {
		fmt.Fprintln(stderr, "The app controller's identity changed. Reopen Hire before using this service.")
		return 2
	}
	_ = conn.SetDeadline(time.Now().Add(4 * time.Minute))
	if err := json.NewEncoder(conn).Encode(in); err != nil {
		fmt.Fprintln(stderr, "App request delivery is uncertain. Inspect the task's app activity before continuing.")
		return 125
	}
	var response appCallResponse
	if err := json.NewDecoder(io.LimitReader(conn, 10<<20)).Decode(&response); err != nil {
		fmt.Fprintln(stderr, "No trustworthy app response. Inspect app activity; do not repeat the operation automatically.")
		return 125
	}
	if response.Error != "" {
		fmt.Fprintln(stderr, strings.TrimSpace(response.Error))
	}
	_ = json.NewEncoder(stdout).Encode(response)
	return response.Exit
}
