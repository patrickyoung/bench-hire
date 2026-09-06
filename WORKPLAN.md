# Hire improvement work

The goal is a clear manager's workspace from hire to retire, with a short path
to useful work, reliable execution, accessible information, and practical help
when a worker needs improvement. Existing Agent homes and Tend remain the
execution and evidence boundaries.

Keep the product centered on the job, tasks, results, and improvement. Hone
supports the requested learning capability. Additional Bench programs remain
available for jobs that need them; an installed program does not require its
own Hire screen or make a worker more capable by itself. Connector setup is
job-dependent, not a new general administration project. Specialist work
should be a bounded way to obtain another perspective through Agent.

## Required outcomes

- [x] Saved work reaches Tend after interrupted or failed submission, without
  repeating started or uncertain work. Planned batches preserve all intent.
- [x] Resolving an uncertain result uses a request-specific completion check
  through real Tend; fake and real adapters agree on the contract.
- [x] Background updates preserve expanded details, drafts, focus, selection,
  scroll position, and current navigation. Errors are visible and recoverable.
- [x] A manager can hire, give a first representative task, inspect what was
  checked, accept a useful result or request changes, and refine the worker.
- [x] Execution verdicts, manager acceptance, and worker readiness are distinct;
  passing structural checks never claims business quality.
- [x] Team and worker views show the next useful action, current work, outcomes,
  schedules, and things needing attention in concise, accessible language.
- [x] Pause and retirement have explicit behavior; retired workers retain
  accessible history and files and cannot receive or start new work.
- [x] A simpler builder can be compared with the review team using recorded
  elapsed time, model calls, corrections, and accepted outcomes.
- [x] Three usable example workers (reporting, document processing, software
  maintenance) include sample tasks, meaningful checks, and recovery exercises.
- [x] Provider capabilities and credential handling respect the shared tool
  boundaries; read access is accurately explained at the permission decision.
- [x] The standing job description is distinct from task instructions and
  task-specific acceptance criteria. Each run records the definition it used.
- [x] A worker can use and develop skills, retain useful memory, act through
  tools, and draw on specialist perspectives. Hire exposes those capabilities
  and their evidence through the existing Bench commands, including reviewed
  learning and creation of reusable skills, without adding a second runtime.
- [x] Offline component tests, opt-in tests using real Bench programs, and
  browser interaction/accessibility checks cover the complete manager journey.
- [x] README and DESIGN describe the delivered behavior and its limits.

## Verification

Required baseline: `go test ./...`, `go vet ./...`, `node --check web/app.js`.
Add race checks for changed concurrent paths and real composition tests for
submission, restart, uncertainty, acceptance, and retirement. Browser checks
must exercise refresh while reading and editing, navigation, and the full
manager journey. Live model evaluations must be identified separately from
deterministic fixture checks; no fixture result claims model quality.

## Final evidence audit — 6 September 2026

| Required behavior | Evidence |
| --- | --- |
| Durable intake and interrupted plan recovery | `TestSavedWorkRecoversSubmissionFailure`, `TestAcceptedPlanFinishesInstallingAfterInterruption`, real Tend submission contract. |
| Uncertain completion checks the exact request | `TestUnknownResolutionChecksExactRequest`, `TestTendSubmissionAndResolutionContract`, real-suite recovery and specialist cases. |
| Readable information survives refresh and navigation | Chromium manager, learning and desktop/mobile reading flows: disclosures, drafts, slow saves, focus, selection, scroll, reload, stale acceptance and navigation. |
| Hire, first task, correction, acceptance and improvement | Full manager browser journey and live sales correction; document and maintenance results reviewed against inputs. |
| Checks, acceptance and readiness stay distinct | `TestManagerReviewAndRetirement`, `TestCompleteResultIsRequiredForReview`, selected-text stale-result rejection; a live report passed arithmetic checks but required a prose correction. |
| Concise team and worker actions, schedules and attention | Browser journey covers task status, revision handoff, add/pause/resume/delete routine, keyboard tabs and labels. Mobile result visually inspected. |
| Pause and retirement | Lifecycle admission tests, scheduler edit/delete race cases, retirement archives, and `TestRetirementStopsALateClaimWithoutStartingAgent`; exit 20 is distinct from Ply approval exit 3. |
| Builder effort and outcomes | Deterministic method comparison plus one live paired trial; single author stays the default. |
| Three useful examples | Behavioral checks reject realistic wrong results; real Agent validation and three live first tasks, including a reviewed correction. |
| Tool/provider ownership and permissions | Pinned tool selection test, `TestAskOwnsProviderSupportAndAuthentication`, exact model proof tests; read scope explained when hiring, applying and changing access. |
| Standing job separate from individual tasks | `TestRunKeepsJobDescriptionSeparateFromTask`, edit/execution locking, exact starting definition/check evidence and revision links. |
| Skills, memory, actions, learning and specialists | Component and browser capability journeys; real Agent/Hone/Brief learning and real specialist execution/evidence. May retains terminal approval. |
| Verification and documentation | Full Go suite, race detector, vet, JavaScript syntax, real-suite composition, all three browser flows; README, DESIGN, BENCH_TOOLS and EVALUATION updated. |

[The live evaluation](EVALUATION.md) records methods, limits, exact synthetic
inputs, retained results, definitions, metrics and review receipts. A single
author took one drafting call and reached an accepted first report. The review
team took six drafting calls; its task stopped unfinished without a result.
These observations do not establish a general model-quality ranking.

Hone preparation declined to retain a lesson from the successful maintenance
run. Hire now explains this ordinary no-lesson outcome clearly and leaves
skills unchanged. Learning admission and subsequent discovery remain proven
by deterministic real-suite composition; an improvement in later task quality
from a learned skill has not been demonstrated.

## Boundaries of the delivered work

- The complete manager workflow is implemented and verified. The evaluation
  uses small known tasks; broader worker competence and benefits from multiple
  perspectives or learned skills require task-specific evidence.
- The five-minute creation path is a design target, not a measured new-user
  service guarantee. No human usability study or formal accessibility
  conformance assessment was performed.
- Hire is one local controller. It does not coordinate simultaneous controller
  processes over the same data root. Agent homes and Tend remain authoritative.
- Run evidence records starting job and executable check, with a final
  comparison. Ask owns effective model context and session evidence; Hire does
  not snapshot every skill, source or mutable artifact.
- Read/write confinement and terminal approvals retain the documented Bench
  boundaries. Optional connectors do not become universal worker dependencies.
- The evaluation did not commit, push or deploy changes. Existing workers and
  the unrelated Ask working tree were not modified by this evaluation.
