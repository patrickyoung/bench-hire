# Bench Hire

Describe a job in a form, press Deploy, and have a digital worker with its own
workspace, schedule, and inbox running under the Bench tools within five
minutes.

## What it is

Hire is a local web controller over two programs that already exist: `agent`
gives a worker a directory-shaped home (`GOAL.md`, `AGENTS.md`, `bin/check`,
`work/`, `state/`, checkpoints, replayable runs), and `tend` makes one command
crash-durable with absolute-time scheduling, retry, and an explicit `unknown`
state. Hire adds only what neither has: a five-minute creation path, an inbox
that turns a request into scheduled actions, a routine scheduler, and one page
per worker that shows work, state, files, and evidence. There is no model in
Hire itself; judgment lives in `ask` (planning) and `ply` (doing), reached
through `agent run`.

## Requirements

- [ ] A person with the suite installed creates and deploys a worker from one
      form in under five minutes without reading Bench source or writing a
      check by hand. Deploy means `agent check` passed and the worker accepts
      work; it never means a model was consulted.
- [ ] Every worker is an ordinary `agent` home on disk. A CLI user can `cd`
      into it, run `agent show`, edit the Markdown, and Hire keeps working.
- [ ] A request arrives as text and becomes one or more actions with a time.
      Each action is one `tend` job whose exact argv re-runs the same request;
      "now" runs immediately, a later time waits durably, and a repeat becomes
      a routine.
- [ ] Done is a program's opinion: `bin/check` accepts only when the request's
      `RESULT.md` exists and the request's own optional check command exits 0.
      A run already done costs no model call on a second pass.
- [ ] Interrupted work resumes. Every run carries `-checkpoint <request-id>`,
      so a retry continues the same conversation; a Tend `unknown` attempt is
      shown as needing a person and is never retried automatically.
- [ ] State and files are visible and editable: `work/` deliverables,
      `state/kv/` facts, and the definition files, with `agent check` rerun
      after every definition edit.
- [ ] Hire runs offline. Tests use a fake `agent` and an in-memory job store;
      no model, network, or credential is required to build or verify it.

## Not doing

- **No second runtime.** Hire never talks to a model provider or runs a
  shell command it composed from user text. `agent run` owns the loop, Cage
  owns the boundary, `ask` owns the planning call. Reimplementing any of them
  would create a second place for policy to drift.
- **No daemon that thinks between events.** The scheduler only writes job
  files at their due time; a worker with an empty inbox costs nothing.
- **No database.** Workers, routines, and requests are JSON files a person
  can `cat`; Tend already owns the transactional job state.
- **No automatic retry of `unknown` or exit 2.** An attempt whose effect is
  uncertain needs a person; a full context window is permanent.
- **No auto-approval.** External effects stay `work/actions/` proposals for
  `agent act` and May; a web button never becomes a terminal yes.
- **No multi-host, no accounts, no gateway.** Loopback only, one trusted
  local user, like Studio's local mode.

## The split

| stage | tool | why |
| --- | --- | --- |
| create the home, write GOAL/AGENTS/check from the form | none -- a script | templates plus the person's words; no judgment |
| validate the home | `agent check` | mechanical, model-free |
| turn a request into timed actions | `ask -schema` | judgment, one shot; falls back to one action "now" with no model |
| compute the next due time of a routine | none -- a script | calendar arithmetic |
| queue, wait, retry, resolve, serialize per worker | `tend` | crash durability without a workflow engine |
| do the work of one request until the check agrees | `ply` via `agent run` | the only place a loop earns its cost |
| decide the request is done | `bin/check` | a shell exit status, never model prose |
| show status, files, evidence | none -- a script | reads files and `tend`/`agent` JSON |

## Data

    raw:     var/workers/<slug>/               the agent home; definition files are
                                               human-owned, work/ and state/ are the
                                               worker's, .agent/runs are Ask sessions
             var/hire/<slug>/worker.json       what was asked for at creation
             var/hire/<slug>/requests/<id>.json  what arrived, never rewritten
             var/hire/<slug>/plans/<id>.json   what the planner said, verbatim
             var/tend/                          Tend's own jobs, attempts, events
             var/ask/                           planner sessions (ASK_DIR)
    derived: var/hire/<slug>/routines/<id>.json  next_due advances; rebuildable
             everything the UI shows            computed from the above on read

## Check

```sh
go test ./... && go vet ./... && node --check web/app.js
```

The generated worker check, written into every home, is:

```sh
#!/bin/sh
home=$(cd "$(dirname "$0")/.." && pwd)
id=$(sed -n 's/^id: //p' "$home/REQUEST.md" | head -1)
[ -n "$id" ] || { echo "no current request"; exit 1; }
[ -s "$home/work/requests/$id/RESULT.md" ] || { echo "missing work/requests/$id/RESULT.md"; exit 1; }
check=$(sed -n 's/^check: //p' "$home/REQUEST.md" | head -1)
[ -z "$check" ] || (cd "$home/work" && sh -c "$check" </dev/null) || { echo "request check failed: $check"; exit 1; }
exit 0
```

`REQUEST.md` sits at the home root, outside `work/` and `state/`, so Cage
keeps the model from rewriting its own judge. The state this check never
sees: a home with no `REQUEST.md` (fails closed), an empty `RESULT.md`
(`-s` rejects it), and a request check that touches files outside `work/`
(the person wrote it; Hire shows it verbatim).

## Layout

    hire/
      DESIGN.md
      main.go        config, tool discovery, server, loops
      app.go         HTTP routes
      store.go       JSON files under var/hire
      worker.go      home creation and templates
      request.go     requests, plans, the ask planner and its fallback
      schedule.go    routine cadence arithmetic
      jobs.go        tend CLI adapter and the in-memory fake
      runner.go      tend work loop and routine scheduler
      exec.go        `hire exec HOME REQUEST.json`, the job Tend runs
      files.go       bounded workspace reads and writes
      web/           index.html, styles.css, app.js

## Traps

- The planner `ask` runs with `-d var/ask` and a fresh session each time, so
  a scheduled call never continues a terminal conversation.
- `tend` scrubs the environment. Provider keys reach `agent run` only through
  `TEND_PASS`; Hire passes the documented provider variables and nothing else.
- Requests are immutable and Tend refuses a different definition under the
  same id, so "run again" is a new request, never an edit.
- `agent run` exit 2 is *not done*, and `ask` exit 2 is a full window; Hire
  shows both as "unfinished, continue or stop" rather than retrying.
