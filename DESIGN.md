# Bench Hire

Describe a standing job, approve its definition, and give the worker a first
task. The manager should quickly reach a useful result, understand what was
checked, and be able to correct, improve, pause, and retire the worker.

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

- [x] A person with the suite installed can hire a worker
      without reading Bench source or writing a check by hand: describe the
      job, approve the drafted job description (or hire with exactly what was
      written), and give it work. Hired means `agent check` passed and the
      worker accepts work; it never means a model was consulted for the hire.
- [x] The page speaks to a hiring manager. Five screens (Team, Hire, Worker,
      Task, Settings), one primary action each, plain words for every state,
      and every technical detail (files, digests, commands, logs, reviewer
      findings) behind one disclosure rather than leading a screen.
- [x] Every worker is an ordinary `agent` home on disk. A CLI user can `cd`
      into it, run `agent show`, edit the Markdown, and Hire keeps working.
- [x] A request arrives as text and becomes one or more actions with a time.
      Each action is one `tend` job whose exact argv re-runs the same request;
      "now" runs immediately, a later time waits durably, and a repeat becomes
      a routine.
- [x] Done is a program's opinion: `bin/check` accepts only when the request's
      `RESULT.md` exists, the reviewed structured worker checks pass, and the
      request's own optional check command exits 0.
      A run already done costs no model call on a second pass.
- [x] Interrupted work resumes. Every run carries `-checkpoint <request-id>`,
      so a retry continues the same conversation; a Tend `unknown` attempt is
      shown as needing a person and is never retried automatically.
- [x] State and files are visible and editable: `work/` deliverables,
      `state/kv/` facts, and the definition files, with `agent check` rerun
      after every definition edit.
- [x] A person can describe a worker in conversation and receive a complete
      definition from one author, with independent review available explicitly.
      Review mode uses three permanent Bench reviewers, who read Hire's
      platform contract and the live home as data and report feature fit in a
      closed vocabulary, and by one to three task experts chosen for the
      request. Nothing is written until the person applies the exact proposal.
- [x] Hire runs offline. Tests use a fake `agent` and an in-memory job store;
      no model, network, or credential is required to build or verify it.

The creation flow is implemented; its five-minute target has not been
measured in a study with new human users. See [EVALUATION.md](EVALUATION.md)
for the actual live task and builder outcomes.

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
| design a definition from conversation | one `ask -schema` author; optional router, 3 platform reviewers, 1–3 task experts before authoring | both modes use the same platform dossier, definition validation, and explicit apply; independent reviews use isolated replayable sessions |
| validate the home | `agent check` | mechanical, model-free |
| turn a request into timed actions | optional `ask -schema` | one explicit planning call for independent tasks; errors keep the draft; ordinary intake saves one task without a model |
| compute the next due time of a routine | none -- a script | calendar arithmetic |
| queue, wait, retry, resolve, serialize per worker | `tend` | crash durability without a workflow engine |
| do the work of one request until the check agrees | `ply` via `agent run` | the only place a loop earns its cost |
| decide the request is done | `bin/check` | a shell exit status, never model prose |
| show status, files, evidence | none -- a script | reads files and `tend`/`agent` JSON |
| record acceptance and corrections | none -- a script | manager receipt binds result bytes and job outcome; a correction creates one linked, immutable revision |
| inspect, prepare, review and admit a reusable lesson | `agent learn` through Hone | Hone owns evidence and admission; preparation does not install; exact admission uses no model |

Optional filters should serve a concrete worker's job. Merely packaging a
program does not justify another Hire screen. See [BENCH_TOOLS.md](BENCH_TOOLS.md)
for the tool selection contract and the current development suite.

The default drafting mode is `single`; `review-team` must be selected for a
turn. Each finished or failed turn records elapsed wall time and the number
of Ask invocations started by Hire, including any model connection proof.
Provider retries inside Ask are not counted a second time. Legacy and
interrupted turns without a final measurement stay unknown. Applied sessions
keep effort and follow-up counts, and each task run's definition digest lets
an evaluation associate an outcome with the definition used. The comparison
fixture runs both modes through structural apply, task execution, and result
acceptance; it makes no claim about comparative model quality.

## Job, task, and result

Direct specialists remain Agent homes beneath `agents/`. A request may name
one specialist, whose `REQUEST.md`, check, work and checkpoint are used by
`agent specialist PARENT NAME`. Tend's cwd and Hire's lifecycle/execution lock
remain the parent, so retirement and serialization include specialist tasks.
Definition evidence, result digests and review receipts refer to the child's
actual work; revisions keep the same specialist and its explicit network
choice. The manager can create a child without a model, assign a focused task,
read and review its result, then prepare an ordinary parent task that names the
findings. This neither adopts findings automatically nor counts a child's
accepted result as evidence of parent readiness. Custom executable checks are
recorded verbatim and distinguished from Hire's generated request-aware check.
Additional task checks require the latter contract.

Supplied examples are data under `examples/`: a standing definition, a separate
first task, immutable reference/check files, and any mutable starting project.
The create path installs all files and checks the completed home before
publishing its worker record. The original first task is saved with that record
so a later change to the bundled example cannot change what this worker was
given. Example selection and hiring do not call a model or queue work.

An Agent home supplies standing instructions, skills, memory, programs, and
permissions. A request supplies the particular task and its check. Request
text is immutable; a run uses the standing definition present when it starts.
Before invoking Agent, `hire exec` records that definition, its digest, the
compiled check digest, and the exact request digest under controller-owned
`evidence/<request>/`. A final comparison records whether the definition and
check remained the same. Legacy runs without a record are shown as unknown
historical criteria rather than borrowing today's definition.

Definition edits and learning operations share a per-worker execution file
lock with runs. The lock serializes Hire operations; external same-user CLI
edits are not forced to take it. Start/end comparison detects differing final
files and the page reports that limitation. Ask still owns the actual session
transcript; Hire does not add a second conversation log.

Tend's `done` means the executable check passed. Hire shows that as ready for
review. A manager's acceptance is a separate immutable receipt containing the
full result SHA256 and Tend update time. Changed output invalidates that
receipt for the current result. A preview cannot supply an acceptance digest;
the manager can open the full result up to 32 MiB.

Feedback is task-specific. Each feedback receipt can create one revision task,
whose identity stays the same across concurrent sends and lost responses.
The new task records its source task and review, carries the correction, and
uses a new result directory. The original output remains accessible. Once a
revision is sent, the revision carries the current attention item. A separate
reviewed definition edit or skill admission is required to change future work.

## Admission, retirement, and reading state

An accepted intake batch is saved before requests and routines are installed.
An installation receipt allows recovery without resurrecting a subsequently
deleted routine. The scheduler reconciles saved requests whose Tend jobs are
missing. A recorded attempt, including unknown, failed, or cancelled work, is
never treated as a missing submission. Existing IDs with different commands
are rejected. Unknown resolution runs `hire verify` through Tend and requires
the exact request and recorded check without starting Agent.

Admission, retry, resolution, routine editing and schedule advancement share
retirement's lifecycle lock. The scheduler re-reads a routine under this lock,
so a stale snapshot cannot restore a deleted schedule. Pausing stops new task
admission; already queued tasks may run. Retirement disables schedules and
cancels pending jobs, waits for active work and explicit unknown resolution,
and keeps the home in place as a readable archive. The executor rechecks the
retirement record before starting Agent.
When retirement stops a late claim, `hire exec` returns its own exit 20;
the task view calls it "Did not start". Ply's exit 3 keeps its approval-related
meaning.

The browser preserves disclosure state, text drafts, focus, selection and
scroll across background replacement. Selected text delays replacement so it
can still be copied. The task review digest changes only when the new result
is mounted, so delayed replacement cannot accept unseen bytes. Completed
tasks poll too. Polling and tab-loading error responses use navigation and
generation guards. Drafts
use session storage when available; file edits retain their base digest and
conflicting saves keep the draft. Creating a memory or work file uses exclusive
creation and cannot overwrite an existing name. Mutation responses check their
originating page and tab before changing the view. Form submissions clear only
matching draft values in their original scope; direct edits typed during a
save retain their text and adopt that successful write's digest. Learning
selection stores only the session or proposal identity, then reads the evidence
again through Agent when reopened. Busy learning forms survive tab replacement.
Archive inspection is read-only; preparation and admission retain lifecycle
and execution checks. Results use an escaped, limited Markdown renderer for
tables, code, lists and HTTP(S) source links. Tables and code scroll in keyboard
focusable regions. Desktop and narrow-screen browser checks cover reading,
selection, updated results, stale acceptance, labels and the manager journey.

Hire validates the shape of a model name, not a fixed provider catalogue.
Ask owns supported providers, endpoints and authentication. Conventional API
key absence is not a readiness verdict. A live proof must return exactly
`ok` (ignoring case and surrounding whitespace). The existing Codex CLI adapter
remains the documented compatibility exception. Settings receives the names
of Tend's passed environment from the server, avoiding a second browser list.

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
      intake.go      durable batch acceptance and interrupted installation
      review.go      exact result reviews and linked revision tasks
      evidence.go    per-run definition and check records; execution lock
      lifecycle.go   mutation admission and retirement archive transitions
      capabilities.go skills, memory inventory, and Agent/Hone learning
      checks.go      structured worker checks, compiler, and AI suggestions
      builder.go     expert builder: sessions, router, reviewers, lead, apply
      platform.go    platform dossier: Agent/Hire contracts, feature catalogue,
                     reviewer checklists, live home inventory
      schedule.go    routine cadence arithmetic
      jobs.go        tend CLI adapter and the in-memory fake
      runner.go      tend work loop and routine scheduler
      exec.go        `hire exec HOME REQUEST.json`, the job Tend runs
      files.go       bounded workspace reads and writes
      web/           index.html, styles.css, app.js: Team, Hire, Worker
                     (Work, Improve, Capabilities, Files, Details), Task, Settings
      web/view-state.js preserves reading and draft state across refresh
      web/markdown.js   escaped result tables, lists, code and source links

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
- A page that explains the platform is not a page that hires. The first UI
  led with a hero about agent homes, a sidebar runtime card, digest strips,
  a router → reviewers → lead assembly board, raw Markdown editors, and shell
  fields in the main flow. It was correct and nobody could hire in five
  minutes. The page now leads every screen with the one thing a manager does
  there and folds the evidence under "Under the hood"; the conversation is
  the primary way to create, refine, and fix a worker, and hand-editing is a
  disclosure, not a tab.
- A builder turn outlives the HTTP request that started it. Inside the request,
  a page reload killed minutes of model work with no trace on screen; the turn
  now runs under the server's lifetime, persists progress after every review,
  and the page polls its status.
