# Bench Hire

Describe a worker's standing job, approve its job description, and give it a
small, real task. Inspect the result and help it improve.

The page is written for a hiring manager, not an operator: a **Team** of
workers, a **Hire** conversation that drafts a job description you approve,
one page per worker to **give it work**, read what it delivered, and **improve**
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
Hone adds reviewed learning. Other optional programs are used when a worker's
job needs them. [BENCH_TOOLS.md](BENCH_TOOLS.md) records the selected suite,
component responsibilities, and the local Hone compatibility correction.

```sh
cd bench-hire
HIRE_MODEL=anthropic/your-model go run .
```

Then open `http://127.0.0.1:8790`. State lives under `bench-hire/var/` unless
`HIRE_DATA` says otherwise.

| Variable | Meaning | Default |
| --- | --- | --- |
| `HIRE_ADDR` | Loopback listen address | `127.0.0.1:8790` |
| `HIRE_DATA` | Records, worker homes, Tend root, planner sessions | `./var` |
| `HIRE_BIN_DIR` | `bin/` of one pinned Bench suite | `bench-suite` beside Hire or in its working directory, then `PATH` |
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
`DEEPSEEK_API_KEY`, `CEREBRAS_API_KEY`, their `_BASE_URL` companions, and
`ASK_MODEL`. The allow list also includes the existing Vertex and Codex routing
variables. Settings shows the complete list of names, never their values.
Export the variables supported by your selected Ask in the shell that starts
Hire. Hire checks the `provider/model` spelling; Ask decides whether that
provider, endpoint and authentication are supported. A missing conventional
API key does not rule out a configured gateway or another Ask login method.

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

## From first hire to recurring work

1. **Hire.** Describe an ongoing responsibility, the inputs, and what useful
   work looks like. A visible three-step guide takes you through describing
   the job, reviewing the draft, and assigning the first task. Example jobs
   are shown as cards. Drafting progress is labelled; there is no timer for
   time spent filling in a form. **Hire without a draft** uses your own words.
2. **Assign.** Open a worker's **Tasks** section. Include the inputs and the
   result you need, choose an optional start time, and press **Assign task**.
   **Planning & completion check** provides optional AI splitting and a
   task-specific executable check. Planning stays off by default. Each saved
   task is durable before queue delivery; interrupted delivery can recover
   without repeating a started or uncertain attempt.
3. **Review.** The always-visible **Inbox** collects results and tasks needing
   a decision. The Team overview shows inbox, running-work, and schedule
   counts. A task opens as an authored document with readable headings and
   tables, its supporting delivery files, and a **Save result** action.
   Activity explains recorded attempt outcomes in plain language. Raw process
   messages and standard output are available under labelled diagnostic
   controls, closed by default even for work needing review.
4. **Correct.** Feedback and acceptance are visible alongside the delivery
   on desktop, and below it on mobile with a shortcut from the top. **Request
   revision** saves the feedback and sends a linked correction task. **Save
   feedback only** preserves a note without starting work; **Send a revision
   task** remains available afterwards, including when queue delivery failed.
   Original output and feedback remain available. Acceptance binds the exact
   result bytes and recorded outcome; a changed result must be reviewed again.
   Large results offer a complete view up to 32 MiB before acceptance.
5. **Develop.** **Training** exposes skill, memory, specialist, and learning
   controls directly. **Job description** changes the standing responsibility
   through a reviewed proposal or the direct editor. **Files** previews
   Markdown and TSV deliverables, with a separate edit action. **Tools &
   access** holds model and internet settings, installed programs, external
   action proposals, and retirement. Installing programs and approving
   external actions still use the host and terminal; the UI explains where.
6. **Repeat.** **Schedule** has its own destination. Add instructions, cadence,
   and a start time in the displayed timezone. Every run creates a task with
   a result to review. Add, run, pause, resume, edit, and remove controls stay
   visible. Begin with work you have already reviewed and accepted.

All six worker destinations remain visible on narrow screens. Controls have
at least 44-pixel touch targets, reduced-motion preferences are respected, and
live updates preserve reading position, selected text, and unfinished drafts.

The standing job describes how the worker should handle many tasks. Each task
keeps the particular instructions and acceptance check given to it. A queued
task uses the job description in force when its run starts; the run records
that definition and check so an old result never displays today's criteria as
its evidence. Correcting one result does not silently rewrite the job or add
a skill.

## Supplied examples

For a concrete first job, **Hire → Start with an example** offers a sales
reporter, document action clerk, and software maintenance worker. Each installs
local inputs and a meaningful task check before the worker accepts work. Review
the example, hire it, and load its supplied first task into the task box; hiring
never starts the task automatically. [The examples](examples/README.md) explain
their checks and correction exercises. They test bounded behavior, not general
worker quality.

## Capabilities and learning

The **Training** section shows reusable skills, working memory, and existing
specialist homes. Installed programs are listed in **Tools & access**. You can supply a known method as a
skill or a sourced fact as memory. New names cannot overwrite existing files.
Agent checks a new skill's home structure; try a representative task to judge
whether the method is useful.

With Hone available, choose a recorded run and **Inspect evidence**. A failed
attempt followed by verified recovery can support a lesson. **Draft a lesson**
prepares an artifact through `agent learn -prepare` and opens the exact change
for review before adding it. Inspection and admission make no model call.
Preparing a proposal leaves the installed skill unchanged, and admission
refuses a proposal that changed after review. Hone owns the provenance and
skill admission rules. Compare later tasks before claiming a learned lesson
improved the worker.

Hone may find no useful lesson even after a successful task. Hire explains
that outcome and leaves the skills unchanged; completing more tasks does not
automatically grow the worker's instructions.

The open evidence or proposal is restored when you return to the tab or reload
the page. Hire reads its contents again from Agent; it does not save a second
copy of the evidence in the browser. Preparation stays visibly busy across tab
changes, and a response arriving after you move elsewhere stays with its
original worker. Retired workers retain read access to learning evidence and
proposals; preparing or admitting a lesson is disabled.

Create a specialist in **Training** with its own standing job description.
This calls `agent new` and validates the parent and child without a model call.
Choose the specialist in the normal task composer, give it one focused task,
and include the evidence to examine. It uses the parent's current model, with
internet access off unless explicitly allowed for that task. Existing Agent
specialist homes can also be selected. A custom specialist check is recorded
and shown as custom; a separate task check requires Hire's request-aware
`bin/check`, so it cannot be silently ignored by an unrelated custom script.

Specialist tasks use Tend and `agent specialist`, with their own request,
checkpoint, result and recorded job description. They appear in the parent's
task list and support the same review, revision, retry and retirement flow.
The parent and its specialists run one task at a time. Accepting a specialist
result does not count as an accepted result from the parent. **Use this
perspective in a task** prepares a draft naming the findings for the parent to
evaluate; the manager completes the task and presses Send. No findings are
automatically adopted. Child files and history remain in their Agent homes;
this workflow does not clone the parent's conversation, skills or permissions.

External action proposals remain reviewable files; execution and approval use
Agent, Action, and May from the terminal.

## Pause and retire

Pausing a worker stops new tasks and schedule admission; its already queued
work can still run. Retirement disables schedules and cancels pending jobs.
Active work may finish, and unknown outcomes must be resolved explicitly before
the worker is marked retired. A late claim during retirement cannot start
Agent and appears as **Did not start**. Retired workers stay in the Team archive with readable results, files,
reviews, and history.

Background refresh preserves expanded details, text drafts, focus, selection,
and reading position. Drafts are kept in this browser tab across reloads when
session storage is available. File and definition saves detect changes since
the editor opened and keep the draft on a conflict. Slow saves clear only the
values submitted on their original page. New text typed during a save remains
a draft, and direct editors retain the completed write's version for the next
save.

Results render tables, lists, code blocks and source links. Wide tables and
code blocks can be scrolled with the keyboard without widening the page.
Completed tasks keep checking for updates. While selected text delays a
redraw, acceptance still refers to the displayed result; changed bytes cause
a conflict and require another review. Tab loading errors offer a retry.

## Model connection

A model connection counts as proved once one
real `ask` call has answered through it. Hire gathers that evidence itself the
first time a model is needed (the first draft or check suggestion), records it
per model under `var/hire/model-proofs.json`, and reports a failed test call in
plain words with the real cause. Settings can fetch the receipt early with
**Test**, and switching models never un-proves one.

## Drafting and independent reviews

The Hire page and each worker's **Improve** tab share one persistent
conversation that turns plain-language intent into a complete Hire-managed
definition: the card name and summary, network proposal, six Agent Markdown
files, and structured acceptance checks.

A normal Send uses one schema-bound Ask call with the request, current
definition, and the same platform dossier the reviewers use. A model connection
test adds one small call the first time that model is used. The proposal uses
the same validation and explicit Apply path in both drafting modes.

Choose **Ask for independent reviews** under **Drafting options** when another
perspective is useful. That mode creates a small design team through public
Ask sessions:

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
successful external effect. The builder changes the definition and checks.
Skills, tools, specialist homes, routines, connectors, and learning use their
own explicit operations; a proposed description does not install them.

**Drafting effort** records each completed or failed turn's method, elapsed
time, and calls started by Hire, including model connection tests. Ask owns any
provider retries inside a call. Applied sessions retain these records; the
worker's Tools & access section shows drafting history, follow-up messages, and current
accepted-result and revision counts. Missing historical measurements stay
unrecorded. Task run evidence supplies the definition digest for matching an
outcome to the job description it used.

The offline comparison exercises both methods through the first accepted
fixture result:

```sh
go test -run TestBuilderModesAndRecordedEffort -v .
```

With a proved model and one routed task expert, the fixture uses one author
call for the default mode and six calls for independent reviews. Its canned
answers establish the workflow and accounting only. To judge quality, use the
same representative tasks and model, record needed corrections and acceptance,
and compare the resulting artifacts. More reviewers are not evidence of better
work by themselves.

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
  inbox/                  accepted batches and installation receipts
  evidence/<request>/     job description and checks recorded for each run
  reviews/<request>/      immutable manager judgments of particular result bytes
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

For concurrency, isolated Chromium interactions, and real Bench composition:

```sh
go test -race ./...
HIRE_BROWSER=1 go test -run 'TestBrowserManagerFlow|TestBrowserLearningFlow|TestBrowserReadingFlow' -v .
HIRE_INTEGRATION_BIN_DIR=../bench-suite/bin \
  go test -run 'TestRealSuite|TestTendSubmissionAndResolutionContract' -v .
```

The optional integration tests use real Bench programs with a deterministic
local model endpoint. They prove contracts and evidence flow, not model
quality. [WORKPLAN.md](WORKPLAN.md) records the delivered manager workflow
and its requirement-by-requirement evidence. [EVALUATION.md](EVALUATION.md)
records the separate live sample tasks, correction, builder comparison and
their limits.
