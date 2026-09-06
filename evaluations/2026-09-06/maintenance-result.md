# Repair result: local slug program

## Defect reproduced

Before the change, invoking the starter through POSIX shell with:

    sh project/slug.sh '  Hello,   WORLD!!  '

exited 0 and printed `--hello,---world!!--`. It retained punctuation and did
not collapse or trim separator runs. An empty argument also exited 0, emitted
one newline on stdout, and wrote no diagnostic, instead of taking the required
error path.

The supplied file initially lacked execute permission, so direct invocation
failed with status 126. Reproduction of the starter's transformation defect was
therefore performed with `sh project/slug.sh`, matching the supplied checker.

## Change

Changed:

- `project/slug.sh`
  - validates a missing or empty first argument;
  - performs ASCII-only upper-to-lower conversion under `LC_ALL=C`;
  - converts each run of non-ASCII-alphanumeric bytes to one hyphen;
  - removes leading and trailing hyphens;
  - rejects a result containing no ASCII letter or digit with exit status 2,
    a useful stderr diagnostic, and empty stdout;
  - prints each successful slug followed by one newline;
  - was made executable so both direct and `sh` invocation work.
- `requests/req-20260906-013746-c7b63c6c6be0/RESULT.md`
  - records this repair and its validation.

No files under `tools/` were changed.

## Validation

`sh -n project/slug.sh` passed.

A manual direct-execution matrix passed:

- punctuation, repeated whitespace, and edge separators:
  `  Hello,   WORLD!!  ` -> `hello-world`
- punctuation, underscores, and digits:
  `--Release__42--` -> `release-42`
- an already valid slug remains `already-good`
- embedded newline edge case: `A<newline>B` -> `a-b`
- non-ASCII bytes are separators: `café 東京 7` -> `caf-7`
- empty argument, punctuation-only input, non-ASCII-only input, and missing
  argument each exited 2, left stdout empty, and wrote a stderr diagnostic

The supplied task check is run after writing this report; its outcome is
recorded below.

## Limits

The program processes only the first argument, as requested. Like all Unix
argument-based programs, it cannot receive an embedded NUL byte in an argument.
No publish, push, deployment, or network action was performed.

## Supplied check outcome

`sh ../tools/example-check` exited 0 with no output.
