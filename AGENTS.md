# Bench Hire engineering boundary

Hire is a local web controller over `agent` and `tend`. It is not a second
agent runtime, scheduler engine, or model client.

- Reach Bench only through documented public commands with literal argv.
  Never build a shell command from user text.
- The agent home is authoritative. Hire's JSON under `var/hire` is a record of
  what was asked; the home, Tend, and `.agent/runs` are the evidence.
- Done is `bin/check`'s exit status. Model prose, a green page, or a finished
  process never become a verdict.
- `unknown` attempts and exit 2 are never retried automatically.
- Approval stays with May and `/dev/tty`; a web button never says yes for a
  person.
- Go standard library only, embedded assets, no framework in the browser.
- Tests must not need a model, a network, credentials, or the real suite.

Run `go test ./...`, `go vet ./...`, and `node --check web/app.js` before
handing off a change.
