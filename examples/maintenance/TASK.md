Repair work/project/slug.sh. It receives text as its first argument and prints
one ASCII slug followed by a newline. Lowercase A–Z, keep ASCII letters and
digits, replace every run of other characters with one hyphen, and remove
leading and trailing hyphens. It must work with punctuation and repeated
whitespace, not just a single space.

With no argument, an empty argument, or input that contains no ASCII letters or
digits, exit 2, write a useful message to stderr, and leave stdout empty. Use
POSIX shell and standard Unix tools; do not add dependencies or network calls.

Reproduce a failing case first. Repair the program, run the supplied task check,
and write RESULT.md for this request describing the defect, change and tests.
Keep the tests under tools/ unchanged. The repaired program is a shared work
artifact; later revisions may improve it while earlier written reports remain.
