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
      `RESULT.md` exists, the reviewed structured worker checks pass, and the
      request's own optional check command exits 0.
      A run already done costs no model call on a second pass.
- [ ] Interrupted work resumes. Every run carries `-checkpoint <request-id>`,
      so a retry continues the same conversation; a Tend `unknown` attempt is
      shown as needing a person and is never retried automatically.
- [ ] State and files are visible and editable: `work/` deliverables,
      `state/kv/` facts, and the definition files, with `agent check` rerun
      after every definition edit.
- [ ] A person can describe a worker in conversation and receive a complete
      definition reviewed by three permanent Bench reviewers, who read Hire's
      platform contract and the live home as data and report feature fit in a
      closed vocabulary, and by one to three task experts chosen for the
      request. Nothing is written until the person applies the exact proposal.
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
| suggest stable worker-level acceptance evidence | `ask -schema` | one bounded proposal; a person applies or dismisses it |
| compile reviewed worker checks | none -- a script | closed structured kinds, validated paths, literal quoting; no model or free-form shell |
| design a definition from conversation | `ask -schema` × router, 3 permanent platform reviewers, 1–3 task experts, lead | judgment, in isolated replayable sessions; the platform contract and live home are Hire-owned data, findings use a closed vocabulary, and a person applies the exact proposal |
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
# Reviewed file/content/size checks from CHECKS.json are compiled here.
check=$(sed -n 's/^check: //p' "$home/REQUEST.md" | head -1)
[ -z "$check" ] || (cd "$home/work" && sh -c "$check" </dev/null) || { echo "request check failed: $check"; exit 1; }
exit 0
```

`REQUEST.md` sits at the home root, outside `work/` and `state/`, so Cage
keeps the model from rewriting its own judge. The state this check never
sees: a home with no `REQUEST.md` (fails closed), an empty `RESULT.md`
(`-s` rejects it), and a request check that touches files outside `work/`
(the person wrote it; Hire shows it verbatim).

The Edit worker page adds a separate, explicit check-design step. The proved
worker model may inspect the saved GOAL.md and AGENTS.md and return only
schema-bound file-nonempty, exact-text, and minimum-byte suggestions. It is
prompted not to invent brittle filenames, wording, sizes, dates, or quality
claims. The user sees plain-language explanations and must apply the proposal.
Hire validates every path as relative to `work/`, supports only the literal
`{request_id}` placeholder, writes the readable list to controller-owned
`CHECKS.json`, deterministically compiles `bin/check`, and reruns `agent check`.
Manual edits use the same structured API. No assistant response is executable
and no worker can write either file under Cage.

## Layout

    hire/
      DESIGN.md
      main.go        config, tool discovery, server, loops
      app.go         HTTP routes
      store.go       JSON files under var/hire
      worker.go      home creation and templates
      request.go     requests, plans, the ask planner and its fallback
      checks.go      structured worker checks, compiler, and AI suggestions
      builder.go     expert builder: sessions, router, reviewers, lead, apply
      platform.go    platform dossier: Agent/Hire contracts, feature catalogue,
                     reviewer checklists, live home inventory
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
- Killing a wrapper is not killing the work. `hire ask` spawns the real `ask`;
  cancelling only the wrapper left the child spending tokens and holding the
  stdout pipe until it finished on its own, and the first error reported was
  the lowest-numbered victim rather than the reviewer that failed. Every
  controller command now runs in its own process group, the group is killed
  on cancel, and a review failure names its cause.
- A readiness gate a person must click is friction, not evidence. The first
  version made "prove the model" a button that disabled the expert team and the
  check assistant until pressed, kept one proof for one model (so switching
  models un-proved the other), and reused the same "prove" wording for an
  unrelated stale-draft condition. Hire now runs the single test call itself
  the first time a model is needed, keeps proofs per model, and says "start
  over" when a draft is stale.
- A cautious lead is not a safe lead. Left to itself the lead builder turned
  "human follow-ups" into gates: person-installed libraries, approval records,
  validators, and reviewed request checks that the worker must refuse to work
  without, plus compiled checks on files the worker never writes. The result
  was a worker that could not complete any request. The lead and the platform
  checklists now require a working path with what is installed today, forbid
  checks on person-supplied files, treat provenance controls as opt-in, and
  cap definition size; every apply keeps the definition it replaced so Revert
  is one action.
- A builder turn outlives the HTTP request that started it. Inside the request,
  a page reload killed minutes of model work with no trace on screen; the turn
  now runs under the server's lifetime, persists progress after every review,
  and the page polls its status.
