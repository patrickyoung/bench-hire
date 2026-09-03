# Bench Hire

Create and deploy a digital worker in five minutes. Describe the job, press
Deploy, and give it work.

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

1. **Describe.** Name the worker and say what it does. Optionally give it a
   routine (instructions plus a cadence) and allow network access.
2. **Deploy.** Hire runs `agent new`, writes `GOAL.md`, `AGENTS.md`, and the
   request-aware `bin/check`, then proves the home with `agent check` and shows
   the exact definition and check digests from `agent show`. No model is
   consulted.
3. **Give it work.** Type a request. With a proved model, one `ask` call turns
   it into actions with times; otherwise it runs as one action now. Each action
   is a Tend job that runs `hire exec HOME REQUEST.json`, which writes the
   request to the home root and calls
   `agent run -checkpoint <request-id> HOME -- …`.
4. **Watch.** The worker page shows queued, running, done, and needs-attention
   work, every attempt's stdout and typescript, the `RESULT.md` it wrote, and
   its `work/` and `state/` files.
5. **Change it.** "Edit worker" on the worker page opens `GOAL.md`,
   `AGENTS.md`, and the other definition files; saving reruns `agent check`.
   The same page can ask the proved model to suggest stable acceptance evidence
   from the saved definition. A person reviews the suggestions, and Hire compiles
   only supported structured checks into `bin/check`; the model never writes
   shell or applies its own proposal. Checks can also be added or removed with
   the structured editor. Routines are edited in place (instructions, cadence,
   check), and the model or network grant can be changed at any time.
   Recorded requests keep the model they were created with.

Prove the model once from Setup; the receipt is one real `ask` answer, and
readiness is never inferred from configuration presence.

## Expert agent builder

The new-worker page and each worker's **Edit worker** tab include a persistent
conversation that turns plain-language intent into a complete Hire-managed
definition: the card name and summary, network proposal, six Agent Markdown
files, and structured acceptance checks.

One explicit Send creates a small design team through public Ask sessions:

1. A router selects one to three task-domain reviewers for the latest request.
2. Bench platform, evidence/check, and authority/reliability reviewers always
   join them. Their platform contract is owned by Hire rather than recalled
   from model memory.
3. Each reviewer gets an isolated, replayable session. At most three run at
   once, and any required review failure stops the turn without replacing the
   last good proposal.
4. A fresh lead session receives the reports as untrusted advisory evidence
   and produces the only definition proposal.

The roster, concise findings, risks, proposal, failed turns, and session names
are persisted under `var/hire`. No expert applies its own work. **Apply** writes
the exact reviewed proposal, refuses stale edits, and rolls back if `agent
check` rejects it. That check establishes structural validity only; it does
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
