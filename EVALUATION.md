# Manager workflow evaluation — 6 September 2026

The delivered workflow reached useful, reviewed results with live model calls.
The evaluation supports keeping a single author as the default and treating
learning and additional reviewers as choices whose value must be checked.

## Method

Three fresh example workers and two separate builder workspaces used
`openai-codex/gpt-5.6-sol` through the existing Hire/Ask adapter, real Tend,
Agent, Ply and Cage, and the corrected Bench 0.13.0 development suite described
in [BENCH_TOOLS.md](BENCH_TOOLS.md). No existing worker was run or changed.
Tasks used synthetic local records, with task internet access disabled.

A temporary wrapper capped each Ply run at 12 model turns and 3 rejected
cycles; Tend capped a job at eight minutes. These are evaluation limits, not
new Hire defaults. A separate model-connection proof preceded each builder
measurement. Wall time includes queue admission and polling overhead. Calls
and responses are not dollar-cost estimates.

Codex reviewed the actual artifacts against the supplied records and program
contract before recording evaluation acceptance through Hire. Passing a check
alone did not produce an acceptance receipt. The original worker's manager
has not personally accepted these synthetic evaluation results.

The [protocol](evaluations/2026-09-06/protocol.json) records the exact common
builder prompt, first task and task check. [Metrics and review receipts](evaluations/2026-09-06/metrics.json)
bind the sampled results and starting definitions. Retained artifacts contain
synthetic inputs and outputs, without credentials or token caches. Live
evaluation is separate from the offline test suite.

## First useful tasks

| Task | Time to first checked result | Review |
| --- | ---: | --- |
| Sales reporting | 73 seconds | Correct period, refunds and regional total of 32,000 cents. Requested a correction because the prose invented a dollar symbol. |
| Document extraction | 49 seconds | Accepted: retained source IDs, replaced the superseded deadline and disclosed the missing deadline. |
| Program repair | 107 seconds | Accepted: POSIX slug behavior met the contract; five additional newline, Unicode and invalid-input cases passed. |

The [sales revision](evaluations/2026-09-06/reporting-revision-result.md) took
47 seconds, preserved the correct TSV and explicitly stated that the currency
was unknown. It was then accepted. The original report and feedback remain
available. Neither this correction nor its acceptance changed the standing
job or installed a skill. All three supplied check scripts retained their
original bytes.

The [document result](evaluations/2026-09-06/documents-result.md) and
[maintenance result](evaluations/2026-09-06/maintenance-result.md) show the
other reviewed outputs. These small samples establish those particular
outcomes, not general worker competence.

## Builder comparison

Both methods received the same job request and ran the same first task, with
the same explicit GBP currency and independent TSV check.

| Method | Drafting calls | Drafting time | First task |
| --- | ---: | ---: | --- |
| Single author | 1 | 59 seconds | Checked result in 61 seconds; accepted after review. |
| Independent reviews | 6 | 159 seconds | Stopped unfinished after 12 seconds and three unsuccessful cycles; no result or acceptance. |

The team added six checks for report labels. Those check structure, not
arithmetic. Both drafted definitions passed Agent's structural validation.
The team worker announced intentions but produced no executable action or
result before the cycle limit. Its [recorded attempt](evaluations/2026-09-06/review-team-attempt.txt)
remains unfinished; it was not silently retried. The single-author worker also
needed internal recovery before it used the shell and completed its report.

This is one paired trial with stochastic model behavior and explicit limits.
It does not establish that a single author is generally superior or that the
team definition caused the execution failure. It provides no evidence that
making the review team mandatory would improve this job. The simpler default
has a demonstrated useful outcome at lower drafting effort.

## Learning

Hone's model-free inspection found an eligible failed-then-passed sequence in
the repair session. Its visible stumble was the initially missing RESULT.md,
which alone says little about a useful repair method. Preparation then returned
exit 1, `nothing worth keeping`. No proposal or skill was installed.

That result supports keeping a review boundary and allowing no lesson as an
ordinary outcome. Hire now explains it in plain words. This evaluation does
not show a learned skill improving a later task. Real-suite deterministic
tests separately prove exact proposal review, admission and skill discovery.

## Interface and reliability

The complete Go suite, race detector, Go vet, JavaScript syntax checks,
real-suite composition tests and Chromium manager, learning and reading flows
pass. Browser checks cover desktop and 480-pixel layouts, labels and keyboard
access, disclosures, selection, navigation, slow saves, drafts across reload,
stale-result rejection, corrections, schedules, specialists and retirement.

The visual reading audit showed tables, code and source links readable on a
narrow screen without horizontal page overflow. These are implementation and
interaction checks; no timed study with new human users or formal accessibility
conformance assessment was performed.
