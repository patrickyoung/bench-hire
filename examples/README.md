# Supplied first tasks

Open **Hire → Try a supplied example**. Review its standing job, first task,
inputs, and check, then hire it. On the worker page, **Use the supplied first
task** fills the task box and its check. Read them before pressing Send.

Hiring an example uses no model call. Running its task needs a working model
connection and the normal Bench execution tools. These jobs use local data,
POSIX shell, and standard Unix commands; they need no connector or internet
permission for their work.

| Example | Useful output | What its first check establishes |
| --- | --- | --- |
| Sales reporter | A regional sales report and a TSV table | Recalculates the requested period, signed refunds and every regional total from the supplied CSV. |
| Action clerk | A sourced action list and unanswered question | Compares the extraction with this email fixture, including a corrected deadline and an explicitly unknown date. |
| Maintenance worker | A repaired local slug program and test report | Runs behavior checks for case, punctuation, repeated whitespace, ordinary slugs and invalid/empty input. |

The `GOAL.md` and `AGENTS.md` files describe the standing job. `TASK.md` names
the specific first task. Files under `home/` are installed into the new Agent
home: references under `inputs/`, executable checks under `tools/`, and mutable
project files under `work/`.
The task check is invoked as `sh ../tools/example-check` from `work/` and uses
the current request ID. It is not a worker-wide check for every future task.

The examples deliberately include errors worth detecting: refunds, a later
correction, and a buggy program. Each `RECOVERY.md` describes a follow-up or
failure exercise. Read the original and revised results, then record acceptance
only when the result meets your needs. Keep a one-task correction separate from
a standing instruction or reusable skill.

These fixtures are small and known. Their checks verify particular output or
program behavior. They cannot establish narrative quality, general competence,
or successful external actions. Passing them once is not evidence that a method
should become a skill. Hone additionally requires a recorded, replay-verified
recovery before proposing a lesson.

The component checks reject realistic wrong outputs before accepting corrected
ones, without using a model:

```sh
go test -run 'TestExamples|TestConcurrentExample' -v .
```

Real Agent/Brief home validation can be checked with:

```sh
HIRE_INTEGRATION_BIN_DIR=../bench-suite/bin go test -run TestRealSuiteExampleHomes -v .
```
