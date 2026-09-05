# Bench Hire

Hire a digital worker in five minutes. Describe the job, approve the job
description, and give it work.

The page is written for a hiring manager, not an operator: a **Team** of
workers, a **Hire** conversation that drafts a job description you approve,
one page per worker to **give it work**, read what it delivered, and **refine**
it in the same conversation when it needs a fix or a new ability. Everything
technical (files, digests, commands, logs) sits one disclosure away under
"Under the hood" and never leads a screen.

A digital worker here is exactly the thing the Bench suite already knows how
to run: an [`agent`](../agent) home with its own `work/` and `state/`, a
`bin/check` that decides when a request is done, replayable Ask sessions under
`.agent/runs/`, and a named checkpoint per request so an interrupted run
resumes the same conversation. [`tend`](../tend) makes every run crash-durable,
schedules later ones by absolute time, and holds an uncertain attempt in
`unknown` until a person decides. Hire adds the five-minute front door, an
inbox that turns a request into scheduled actions, a routine scheduler, and one
page per worker for its work, files, state, and evidence.

Hire has no provider client or agent runtime in it. Planning and the optional
expert builder use schema-bound `ask` calls; doing is `ply` inside `agent run`,
confined by Cage. See [DESIGN.md](DESIGN.md) for the requirements, the split,
and what Hire refuses to do.

## Run

Requires Go 1.26 or newer and the Bench suite on `PATH` (`agent`, `tend`,
`ask`, `ply`, `brief`, `cage`; see [bench/install.sh](../bench/install.sh)).

```sh
cd hire
HIRE_MODEL=anthropic/your-model go run .
```

Then open `http://127.0.0.1:8790`. State lives under `hire/var/` unless
`HIRE_DATA` says otherwise.

| Variable | Meaning | Default |
| --- | --- | --- |
| `HIRE_ADDR` | Loopback listen address | `127.0.0.1:8790` |
| `HIRE_DATA` | Records, worker homes, Tend root, planner sessions | `./var` |
| `HIRE_BIN_DIR` | Absolute `bin/` of one pinned Bench suite | resolved from `PATH` |
| `HIRE_MODEL` | Default `provider/model` for new workers and the planner | `$ASK_MODEL` |
| `HIRE_WORKERS` | Concurrent `tend work` loops | `2` |
| `HIRE_JOB_MAX` | Longest one run may take before Tend stops it | `45m` |
| `HIRE_JOBS` | `memory` keeps jobs in memory for development | Tend |
| `HIRE_WEB_DIR` | Serve the page from this directory instead of the embedded copy (development) | embedded |

Build the binary rather than `go run` for anything you want to survive a
restart: Tend records the exact `hire` path in every job, and `go run` deletes
its temporary binary on exit.

```sh
go build -o hire . && HIRE_MODEL=openai-codex/gpt-5.6-sol ./hire
```

Provider credentials reach a run only through Tend's `TEND_PASS` allow list:
`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GEMINI_API_KEY`, `OPENROUTER_API_KEY`,
their `_BASE_URL` companions, and `ASK_MODEL`. Export them in the shell that
starts Hire.

## Codex (ChatGPT login)

`openai-codex/…` models authenticate with an OAuth token that `ask` accepts
only on descriptor 3. Hire installs `var/bin/ask`, a wrapper that runs
`hire ask`, and hands it to `agent` as `AGENT_ASK`, so every `ask` call inside
a run goes through it:

- For any other provider the wrapper is an exact pass-through.
- For `openai-codex` it reads the official Codex CLI login at
  `~/.codex/auth.json` (or `$CODEX_HOME/auth.json`), the same file the earlier
  `ask` imported. Log in with `codex login`; Hire never logs in.
- When the access token is within five minutes of expiry, Hire refreshes it
  with the standard refresh-token grant and keeps its copy in
  `var/hire/codex-token.json` (mode 0600). The Codex CLI's own file is never
  modified. Whichever copy expires later is used.
- The header travels on a pipe attached as descriptor 3 of the `ask` process,
  never through argv, the environment, or a session log. The non-secret
  `ChatGPT-Account-Id` is passed as `OPENAI_CODEX_ACCOUNT_ID` when unset.

Set `HIRE_OAUTH_PROFILE=NAME` to use an `oauth` profile instead; the wrapper
then runs `oauth with NAME -- ask -header-fd 3 …` and the Codex CLI file is
not consulted.

## The five-minute path

1. **Describe.** The Team page with no workers is one prompt: say what you
   need done. With a model configured, Hire drafts a job description with a
   small review team; while it works the page shows one calm progress card
   ("Drafting the job description", reviews in, elapsed time) and you can
   leave or reload. Without a model, **Hire without a draft** takes exactly
   what you wrote.
2. **Approve.** The draft arrives as a job description card: the name, what
   done looks like, how the worker will work (folded), how Hire knows a task
   is done, what changed in this draft, and who reviewed it (folded). Reply
   to change anything, or press **Hire**. Hire creates the agent home, writes
   the files, compiles the checks, proves the home with `agent check`, and
   opens the worker's page. No model is consulted for the hire itself.
3. **Give it work.** The worker page leads with one box: give the worker
   something to do. **Options** holds the rest: let AI split the request into
   steps and timings (one schema-bound `ask` call), start no earlier than a
   time, an optional done check. Each step is a durable Tend job that runs
   `hire exec HOME REQUEST.json`, which writes the request to the home root
   and calls `agent run -checkpoint <request-id> HOME -- …`.
4. **Watch.** The Work tab lists tasks in plain words (Working, Waiting to
   start, Scheduled, Done, Stopped early, Not accepted, Outcome unclear) and
   any recurring tasks with their next run. A task page shows the result
   first, then what you asked, then what happened (each attempt with its log
   folded), with the exact command and request file under "Under the hood".
   Anything that needs a decision appears under **Needs you** in the top bar
   and on the Team page; nothing there is retried on its own.
5. **Refine.** The worker's Refine tab is the same conversation, scoped to
   this worker: "also keep a list of…", "stop and ask me when…", or, from a
   stopped task, **Refine** prefills the message with what went wrong. The
   proposal arrives as a job description card with **Apply**; an apply can be
   reverted with one action until something else changes. **Edit the job
   description by hand** opens the files, the name and summary, and the done
   criteria (structured checks, suggested by the model or added yourself).
   The Details tab holds the model, internet access, the folder on disk, the
   digests, `agent show`, the compiled check, and the run history.

Readiness is evidence, not configuration: a model counts as proved once one
real `ask` call has answered through it. Hire gathers that evidence itself the
first time a model is needed (the first draft or check suggestion), records it
per model under `var/hire/model-proofs.json`, and reports a failed test call in
plain words with the real cause. Settings can fetch the receipt early with
**Test**, and switching models never un-proves one.

## Expert agent builder

The Hire page and each worker's **Refine** tab share one persistent
conversation that turns plain-language intent into a complete Hire-managed
definition: the card name and summary, network proposal, six Agent Markdown
files, and structured acceptance checks.

One explicit Send creates a small design team through public Ask sessions:

1. A router reads the request and selects one to three task-domain experts
   for it (an accountant, a support-operations lead, a release engineer, and
   so on), each with a focus naming the decisions it should review.
2. Three permanent Bench reviewers always join them: the platform architect,
   the evidence and acceptance architect, and the authority and reliability
   reviewer. Each has its own checklist. Every reviewer receives the same
   platform dossier as data rather than recalling Bench from memory: the
   Agent home contract with the installed suite's exact limits and exit
   codes, Hire's operating contract (`REQUEST.md`, `RESULT.md`,
   `CHECKS.json`, routines, `-net`, per-request checkpoints, Tend outcomes),
   the compiled `bin/check` for the current proposal, the Bench feature
   catalogue, the installed tool versions, and, when editing, the live home:
   `agent show`, installed skills, tools, specialists, state keys, and
   routines.
3. Each reviewer gets an isolated, replayable session. At most three run at
   once, and any required review failure stops the turn without replacing the
   last good proposal. Platform reviewers return structured platform-fit
   findings (used, missing, misused, not needed) over the feature catalogue
   plus questions only the person can answer. Hire rejects any feature or
   status outside that closed vocabulary.
4. A fresh lead session receives the reports and the same dossier as
   untrusted advisory evidence, must change the definition or explain itself
   for every missing or misused finding, relays at most one question, and
   produces the only definition proposal.

A turn runs on the server, not inside the page request: Send is acknowledged
at once, and you can leave or reload the page while the team works. While it
runs, the page shows one progress card (what stage the draft is at, reviews
in, elapsed time) with the roster and each reviewer's live state folded under
"Who is reviewing". A task expert whose reply fails validation is dropped from
that turn and noted; a permanent reviewer's failure stops the turn, names that
reviewer, and kills the other reviewers' whole process trees so no orphaned
`ask` keeps spending tokens. A failed turn never replaces the last good
proposal, and a turn interrupted by a Hire restart is closed as failed the next
time the page asks about it.

The lead may end a turn with one question and mark the proposal not ready;
**Apply** stays locked until a later turn marks it ready. Answer the question,
or use the "proceed with its stated assumptions" action, which sends that
instruction as the next message.

The roster, findings, platform-fit coverage, risks, questions, proposal,
failed turns, and session names are persisted under `var/hire`. The page
shows the proposal as a job description card, each reviewer's summary and
risks folded under "Reviewed by N specialists", and the lead's message in the
conversation; the structured findings stay in the session file for anyone who
wants them. No expert applies its own work. **Apply** writes
the exact reviewed proposal, refuses stale edits, and rolls back if `agent
check` rejects it. An apply to an existing worker also records the definition
it replaced; **Revert to the previous definition** on the Edit worker tab
restores it through the same checked path, as long as nothing else was edited
after that apply. That check establishes structural validity only; it does
not claim business correctness, production readiness, human approval, or a
successful external effect. Skills, tools, runtime specialist homes, routines,
connectors, and learning remain explicit follow-up surfaces rather than things
the builder pretends to have installed.

## What a worker looks like on disk

```text
var/workers/<slug>/        the agent home, usable from the CLI as-is
  GOAL.md AGENTS.md        written by Hire from your words; edit freely
  CHECKS.json              reviewed worker checks in readable structured form
  bin/check                accepts when work/requests/<id>/RESULT.md exists
                           and worker-level plus request-specific checks pass
  REQUEST.md               the current request; controller-owned, read-only
                           to the model under Cage
  work/requests/<id>/      deliverables, one directory per request
  state/kv/                facts the worker keeps between requests
  .agent/runs/             replayable Ask sessions (agent history)
  .agent/checkpoints/      one checkpoint per request id
var/hire/<slug>/           Hire's records: worker.json, requests/, routines/, plans/
var/tend/                  Tend's jobs, attempts, and event log
var/ask/                   planner and proof sessions
```

Every request is immutable once recorded; "run again" is a new request. A
failed attempt can be retried (same argv, same checkpoint). An `unknown`
attempt is shown under Needs attention with Tend's three resolutions and is
never retried on its own.

Worker-level checks are deliberately narrower than request checks. The check
assistant returns schema-bound `file_nonempty`, `text_contains`, and
`minimum_bytes` conditions over literal paths under `work/` (with an optional
`{request_id}` placeholder). Hire validates those values and quotes them into
the script. This makes the normal path explainable and avoids turning model
output into an arbitrary command. The baseline non-empty `RESULT.md` check
remains even when the reviewed list is empty.

## Verify

```sh
go test ./...
go vet ./...
node --check web/app.js
```

Tests use a fake `agent`, a fake `ask`, and an in-memory job store; no model,
network, or credential is needed.
