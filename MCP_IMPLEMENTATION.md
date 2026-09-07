# Connected apps implementation

User outcome: a hiring manager connects an MCP service, gives selected employees
clear permissions, sees whether it works, and teaches service-specific skills.
The skills flow must support continued improvement using actual work and feedback.

## Product requirements and completion evidence

- [x] Connect a remote URL or local command; support common MCP configuration JSON.
- [x] Authenticate with a token/custom headers, local environment values, or the
  existing OAuth CLI's browser/device/client-credentials flows. Credentials never
  appear in normal API responses, browser drafts, model context or generated tools.
- [x] Discover capabilities without granting or executing them; show readable names,
  descriptions and input requirements. Handle current and explicit legacy clients.
- [x] Assign/revoke capabilities per employee; explain automatic use vs proposals
  requiring May. A service change never silently expands an existing grant.
- [x] A real confined Agent can use granted service capabilities without needing
  unrestricted internet access or being handed service credentials.
- [x] Calls delegate to public Bench MCP commands. Tool effects pass through Action
  and a deterministic operator permission policy; May decisions stay in the terminal.
  Preserve pending/uncertain outcomes and never automatically repeat an effect.
- [x] Connection tests, reconnection, updated capabilities and disconnection are
  understandable and durable. Revocation prevents subsequent calls.
- [x] Record reviewable call results and retained Context/Cite sources, including
  useful error states, without equating a successful call with a completed task.
- [x] Teach the service from its admitted capabilities and optional guides/examples.
  Link the service to its installed skills and their existing whole-folder
  improvement workflow. A skill does not silently grant a permission.
- [x] Desktop/mobile browser flows prove the complete manager journey. Offline Go
  tests prove permissions, secret handling, lifecycle, version and failure behavior.
  Opt-in real Bench tests prove discovery, Action receipts, confinement and actual
  worker invocation. Run Go tests, vet, JS syntax checks and rebuild Hire.

## Implementation boundary

Hire remains a controller over public Bench programs. MCPbox owns catalogue
compilation and descriptor-digest admission; MCP owns transports and schemas;
OAuth owns tokens and refresh; Action owns effect authorization and receipts;
Agent/Ply/Tend own work execution. Hire's connected-app command is a client of a
local Unix-socket controller seam, not another model or MCP runtime. It exposes
only the employee's explicit grant. The controller runs the admitted programs
outside the worker's Cage boundary, records the result and supplies cited evidence.
A connection's service configuration is controller state; its employee grant and
small tool entry point live in the Agent home. Skill teaching reuses the existing
source and reviewed-improvement features.

## Verification completed — 2026-09-07

- `go test ./...`, `go vet ./...`, `node --check web/app.js`, and
  `node --check web/connections.js` pass. `go build -o hire .` succeeds.
- `go test -race -run '^TestConnected' .` passes. Default tests cover
  credential handling, OAuth lifecycle, grants and revocation, changed programs,
  uncertain calls and manager observations, retained sources, cited teaching,
  stale skill drafts, and exact large numeric values in redacted JSON.
- `HIRE_BROWSER=1 go test -run '^TestBrowserConnectedAppsFlow' -v .` passes at
  1280, 390 and 320 pixels. It exercises configuration import, secret exclusion
  from browser drafts, discovery, permissions, guide upload, cited skill review
  and installation, whole-folder improvement, revocation and disconnection.
- The opt-in `TestRealSuiteConnectedDiscovery`,
  `TestRealSuiteConnectedHTTPToken`, and `TestRealSuiteConnectedLegacy` pass
  against `../bench-suite/bin`. They cover modern stdio, authenticated HTTP,
  legacy compatibility, resources and templates, Action receipts, Unix peer
  identity, Cage with network disabled, an actual Agent/Ply task, and Cite.
  Changing a service descriptor stops its old admitted call and exposes changed
  access for manager review.
- The Bench suite manifest now includes the existing `mcp-legacy` command;
  `go test ./...` passes in Bench. The rebuilt development archive and extracted
  suite checksums were verified; `../bench-suite` points at that bundle.

Worker app invocation currently requires Linux. May approval remains a terminal
operation. Service snapshots and test fixtures establish source identity and
program behavior; they do not establish the quality of a newly drafted skill.
Managers review that skill and can improve it using representative work results.
