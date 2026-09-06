# Bench tools used by Hire

Reviewed against [patrickyoung/bench](https://github.com/patrickyoung/bench),
commit `c4e02f1cce7b57265493d95964534e7ae774ad44`, and its suite 0.13.0
[manifest](https://github.com/patrickyoung/bench/blob/c4e02f1cce7b57265493d95964534e7ae774ad44/internal/suite/manifest.json).
The component READMEs and public commands were inspected from those exact
source revisions. The suite packages independent programs; it does not add
another worker runtime.

The product goal is to manage a worker's job, tasks, results, and improvement.
Use a Bench tool when a concrete job needs its capability and its public
command saves Hire from implementing the same responsibility. Availability
alone does not justify a new manager screen. Hone belongs to the learning
path; connectors and other optional tools are configured as jobs require them.
Evaluate additions through accepted results, corrections needed, elapsed time,
and model calls, not the number of installed programs or participating agents.

| Responsibility | Appropriate tool | Hire's connection |
| --- | --- | --- |
| Model calls and conversation evidence | Ask | Literal `ask` calls; Agent and Hone receive the same selected executable. |
| Execute a worker's tasks | Agent and Ply | Tend invokes `hire exec`, which prepares the task and runs Agent. Ply owns the loop and executable check. |
| Queue, restart, scheduling, uncertain outcomes | Tend | Public submit/list/show/work/retry/resolve commands. Saved requests reconcile missing submissions. |
| Write and network limits | Cage | Agent's normal confined action path. This limits writes; it does not hide other readable files on the account. |
| Discover, read, and validate skills | Brief | Agent's skill selection and validation; the Capabilities page lists skills through `brief ls`. |
| Learn a procedure from a verified recovery | Hone through Agent | `agent learn -why`, `-prepare`, `-show`, and `-admit`. Preparation does not install a skill; admission uses the reviewed artifact and calls no model. |
| Read and search execution history | Trail through Agent | `agent history`; Ask remains the session owner. |
| Remember facts and current progress | Agent's `state/kv/` and `state/plan.md` | Ordinary worker-owned files. `MEMORY.md` is curated standing context. A working note and a verified procedural lesson are different things. |
| Use another perspective | Agent specialist homes | `agent new` creates a child; `agent specialist PARENT NAME` runs its explicit task through Tend. Hire lists the assignment and separate result under the parent, with review, revision and an explicit draft handoff to the parent. Each child has separate context, skills, permissions, work and evidence. |
| Retrieve outside evidence | Context | Available in the selected suite; specific source connectors must be configured. This is the appropriate source interface for research workers. |
| Check citation identities | Cite | Available for task checks. It checks exact ref/URL pairs against Context records, not whether the cited prose is true or supported. |
| Execute outside effects | Action and May | Agent's `work/actions/` proposals and `agent act` controller path. May retains terminal approval; no web button impersonates a May decision. |
| Connect external applications | MCP, MCPbox, MCPserve | The suite includes the protocol edge. MCPbox admits source/action/tool connectors; discovering a service alone does not grant a worker access. |
| Resource-bound login and refresh | OAuth | The existing `HIRE_OAUTH_PROFILE` route composes `oauth with`. Profiles and credentials remain operator state. |
| Build and prove a larger system | Draft | Included for systems that need a build/prove workflow. Ordinary task execution remains Agent/Ply, and the current Hire builder drafts job descriptions. |
| Terminal management | Bench | A sibling interface over the same filters, useful for inspecting and managing work from a terminal. Hire does not need to invoke its TUI to run a worker. |

The suite's packaging contract identifies Rules, Vouch, and Web as additional
standalone capabilities. They can be added for a concrete repository,
authentication, or browser task; their presence is not a prerequisite for
every worker. Merely installing the complete suite does not create source
connectors, skills, or specialist homes for a particular worker.

## Local installation and selection

Hone's source is at `../hone`; `~/.local/bin/hone` links to its tested build.
The complete suite is unpacked under `../bench-dist/`, with `../bench-suite`
pointing to the current development bundle. This checkout's ignored
`bench-suite` link points there as well.

Hire selects tools in this order:

1. Explicit `HIRE_<TOOL>` overrides.
2. `HIRE_BIN_DIR`, or a `bench-suite` directory with a valid suite marker
   beside the Hire executable or in its working directory.
3. Ordinary `PATH` when no suite directory is selected.

A missing command in a selected suite stays missing; Hire does not silently
substitute a host version. Selected Ask, Ply, Brief, Cage, Hone, Trail, May,
and Action paths are passed to Agent's public environment overrides. The suite
directory is on the child PATH for other filters. Setup reports the resolved
paths, versions, and purposes. Missing optional capabilities do not prevent
an otherwise usable worker from doing an ordinary task.

The existing Codex CLI login adapter remains a compatibility exception:
OAuth explicitly does not import provider-specific CLI logins. Removing that
adapter would break the current login path. Configured OAuth profiles use the
shared filter. The isolated live evaluation used Hire's existing login adapter;
it did not modify the Codex CLI login or add another credential import path.

## Compatibility correction and evidence

The stock pinned Hone used the removed Ask `-n` flag for both lesson wording
and new-skill descriptions. The real integration test caught this after the
worker's execution and recovery evidence had succeeded. Local Hone now uses
fresh, explicitly named `ask -f` sessions. Its fake Ask refuses the obsolete
flag, so the default tests also guard that boundary.

The release archive is preserved. The corrected suite is built separately
with `benchpack -allow-dirty` and is named
`bench-suite-0.13.0-linux-amd64-dirty`; its manifest records Hone as modified
and its archive and file checksums describe the actual build.

Verified locally:

- Hone's `go test ./...` and `go test -race ./...` pass after the correction.
- The suite builder's version, composition, installation, and checksum smoke
  checks pass for the development bundle.
- `TestRealSuiteRecoveryAndLearning` exercises real Hire, Tend, Agent, Cage,
  Ply, Ask, Trail, Hone, and Brief with a local deterministic model endpoint.
  It covers failure then recovery, an actual replayable session, model-free
  evidence inspection, proposal preparation without installing a skill,
  exact review, model-free admission, and discovery of the learned skill.
- `TestRealSuiteSpecialist` creates and validates a real specialist home, runs
  a recovery through Tend and Agent's specialist command, checks that the child
  owns its session and result, and resolves the recorded child completion check.

```sh
HIRE_INTEGRATION_BIN_DIR=../bench-suite/bin \
  go test -run 'TestRealSuiteRecoveryAndLearning|TestTendSubmissionAndResolutionContract' -v .
```

Use an absolute binary directory if running from another working directory.
These tests prove the program contracts and evidence flow. Live task quality
and the usefulness of learned lessons require separate evaluation.
