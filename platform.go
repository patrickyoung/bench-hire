package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// This file is the builder's knowledge of the platform's inner workings. The
// reviewers receive it as data on every turn, so what they know about Agent,
// Hire, Cage, Tend, and the check contract is owned here and versioned with
// Hire, never recalled from model memory.

// benchFeature is one platform capability a worker definition can use. The
// catalogue is the closed vocabulary for structured platform-fit findings.
type benchFeature struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

var benchFeatureCatalogue = []benchFeature{
	{"goal", "GOAL.md outcome", "The durable outcome, concrete definition of done, constraints, and stop conditions the worker pursues on every run."},
	{"agents", "AGENTS.md method", "Inputs, source precedence, working method, output locations, budgets, exceptions, and escalation rules."},
	{"soul", "SOUL.md voice", "Optional persona, tone, and stable values; only when the job has an audience or a voice that matters."},
	{"plan", "PLAN.md strategy", "Optional standing strategy, not current progress; read with the goal on every run."},
	{"memory", "MEMORY.md curated facts", "Optional small, human-curated facts injected into context; never automatic learning."},
	{"heartbeat", "HEARTBEAT.md watch work", "Recurring watch instructions used by agent tick; a nonempty heartbeat needs an executable bin/wake, and cadence stays with Hire routines."},
	{"state_kv", "state/kv durable facts", "One fact per file under state/kv/, kept between requests and inspected on demand rather than loaded wholesale."},
	{"work_layout", "Named work/ deliverables", "Stable, concrete paths under work/ for what the worker produces, so people and checks can find them."},
	{"result", "RESULT.md contract", "work/requests/{request_id}/RESULT.md: one summary line, the result, then what was left undone and why."},
	{"structured_checks", "CHECKS.json evidence", "Reviewed file_nonempty, text_contains, and minimum_bytes conditions over literal work/ paths, compiled into bin/check."},
	{"request_check", "Per-request check command", "An optional shell command a person attaches to one request; it runs from work/ and must exit 0."},
	{"network", "Cage network grant", "Network is denied unless the worker record grants it; then every run uses agent run -net. Markdown cannot widen it."},
	{"actions", "work/actions effect proposals", "External effects stay strict connector-name plus JSON proposals; a person runs agent act through Action and May."},
	{"proposals", "work/proposals definition patches", "A confined worker may propose a one-file unified diff; only agent amend with May approval applies it."},
	{"skills", "skills/ Agent Skills", "Reusable verified procedures Brief selects from the goal; agent-local, not inherited from the host."},
	{"tools", "tools/ programs", "Small deterministic programs on the worker's PATH; the right home for parsing, formatting, and lookups."},
	{"specialists", "agents/ specialist homes", "Nested homes with separate definition, authority, check, and evidence, run only by the controller with agent specialist."},
	{"routines", "Hire routines", "Scheduled instructions that become one request per occurrence; the only cadence mechanism Hire offers."},
	{"checkpoints", "Per-request checkpoints", "Every run carries -checkpoint <request-id>, so a retry resumes the same conversation; an unknown attempt still waits for a person."},
	{"escalation", "Human escalation", "Named conditions under which the worker stops, writes what is missing to RESULT.md, and waits for a person."},
	{"budgets", "Finite budgets", "Explicit limits on sources, attempts, size, and time so a run ends even when the world does not cooperate."},
}

var benchFeatureIDs = func() []string {
	ids := make([]string, 0, len(benchFeatureCatalogue))
	for _, feature := range benchFeatureCatalogue {
		ids = append(ids, feature.ID)
	}
	return ids
}()

var platformFitStatuses = []string{"used", "missing", "misused", "not_needed"}

// agentContract states the Agent home contract with the numbers the installed
// agent enforces (agent 0.2.x: FILE_MAX, CONTEXT_MAX, SKILL_MAX, TOOL_MAX).
const agentContract = `Agent home contract (agent 0.2.x):
- Required: GOAL.md, AGENTS.md, executable bin/check. Optional standing context: SOUL.md, PLAN.md, MEMORY.md, HEARTBEAT.md. Each definition file is at most 32768 bytes and all of them together at most 65536 bytes; agent check refuses larger, symlinked, or unwritable definitions.
- Context order on a run: AGENTS.md, SOUL.md, MEMORY.md, then Brief-selected skills, then the task built from GOAL.md plus the invocation focus. PLAN.md, bin/wake output, and piped input arrive as separate evidence, never as instructions that outrank the definition.
- Directories: work/ (mutable deliverables; the working directory), state/ and state/kv/ (durable facts not injected automatically), tools/ (agent-local programs prepended to PATH, at most 128), skills/ (Brief-readable Agent Skills, at most 64, 32768 bytes each, 262144 bytes total), agents/ (nested specialist homes), .agent/runs (Ask sessions), .agent/checkpoints (conversation pointers).
- bin/check runs from work/ before the first model turn and after every candidate completion. Exit 0 accepts, exit 1 rejects and returns its stdout to the worker as feedback, any other exit means the verifier is broken. A run already accepted costs no model call.
- agent run exits 0 accepted, 1 broken, 2 unfinished (context window full or budget spent). agent tick runs bin/wake first: exit 0 is quiet and spends no model call, 1 wakes, anything else is broken. A nonempty HEARTBEAT.md requires an executable bin/wake.
- Cage default: the worker may write only work/, state/, and a private temporary directory; network is denied unless the controller passes -net. No Markdown file, skill, or tool can grant authority, network, approval, model choice, or completion.
- A confined worker cannot spawn provider calls. Specialists under agents/ run only through the controller command agent specialist PARENT NAME, receive only their explicit task, and keep separate evidence.
- Learning: agent learn offers one replay-verified failed-then-passed session to Hone; admission is an explicit human step and there is no automatic MEMORY.md rewrite.
- Definition changes from a run: the worker writes a one-file unified diff under work/proposals/; only agent amend with a May decision applies it, with hash rechecks and rollback.
- External effects: the worker writes {connector, input} JSON under work/actions/; only agent act outside Cage routes it through Action and May. Review never causes an effect; status 125 means an effect may exist without a receipt and is never retried automatically.`

// hireContract states what Hire adds around an agent home and how work
// actually reaches it.
const hireContract = `Hire operating contract:
- Each worker is one agent home under var/workers/<slug> with GOAL.md and AGENTS.md written from the definition, a controller-owned CHECKS.json, and a compiled read-only bin/check. Hire records the card name, summary, model, and network grant in worker.json; the home is authoritative for everything else.
- Work arrives as requests. Hire writes REQUEST.md at the home root (outside work/ and state/, so Cage keeps it read-only to the model) with single-line headers id:, kind:, title:, check:, result:, created:, then a "# Request" section with the full text. The invocation focus tells the worker to read REQUEST.md and write work/requests/<id>/RESULT.md.
- Every run is one Tend job running: agent run [-net] -m <worker model> -checkpoint <request-id> HOME -- <focus>. The checkpoint name is the request id, so a retry continues the same conversation. Tend scrubs the environment; only provider credentials and Bench override variables pass. A run may take at most HIRE_JOB_MAX (default 45m).
- Done is bin/check's exit status: work/requests/<id>/RESULT.md exists and is non-empty, every CHECKS.json condition holds, and the request's own check command (if any) exits 0 from work/. Model prose never becomes a verdict.
- CHECKS.json supports exactly three kinds over literal paths relative to work/: file_nonempty, text_contains (exact text), and minimum_bytes. {request_id} is the only placeholder. At most 12 checks. Hire validates and quotes them into the script; nobody submits shell through this path.
- Outcomes: exit 0 is done; exit 1 is a broken check; exit 2 is unfinished and waits for a person; a crash after start is unknown and is never retried automatically. Failed attempts can be retried with the same argv and checkpoint.
- Routines: instructions plus a cadence (hourly, daily, weekdays, weekly with a weekday, or a duration between 1m and 168h, with a time of day where relevant). Each occurrence becomes one immutable request. Hire, not the worker, owns the schedule; HEARTBEAT.md is read by agent tick, which Hire does not run today, so recurring work belongs in a routine.
- Planning: with a proved model, one schema-bound ask call turns a free-text request into timed actions; without one, the request runs now as a single action. Recorded requests keep the model they were created with.
- Authority: the network grant is a worker-level record a person changes on the worker page; it becomes -net on every run. Approval stays with May and /dev/tty; a web button never says yes for a person. Credentials never belong in any definition file.
- The builder can write only the six definition files, the card name and summary, the network proposal, and CHECKS.json. Skills, tools, specialist homes, routines, connectors, and learning are follow-up steps a person performs; the definition may name them as required follow-ups but must not claim they exist.`

// platformReviewerContracts gives each permanent reviewer its own checklist,
// so the three platform reviews are complementary rather than three copies of
// one generic review.
var platformReviewerContracts = map[string]string{
	"bench-platform": `You are the Bench platform architect. Review the definition against the Agent home contract and Hire operating contract supplied as platform data. Check every item:
1. Each definition file carries only what belongs to it: GOAL.md outcome and done, AGENTS.md method, SOUL.md voice only when it matters, PLAN.md standing strategy only, MEMORY.md small curated facts only, HEARTBEAT.md watch work only. Flag content in the wrong file.
2. Size: nothing near the 32768-byte per-file or 65536-byte total limit; prefer concise definitions.
3. state/kv is used for facts that must survive between requests, with a named key scheme, and the definition says to inspect state on demand.
4. work/ deliverables have stable, concrete paths that a person and a check can find; the RESULT.md contract is respected.
5. Nothing asks the worker to do what Cage forbids: writing outside work/ and state/, using the network without a grant, editing definition files, invoking connectors directly, or spawning its own model calls.
6. Anything that should be a tools/ program (parsing, formatting, lookups), a skills/ procedure, or an agents/ specialist is named as an explicit follow-up rather than described as if installed.
7. Recurring work is expressed as a Hire routine, not as a schedule inside Markdown; HEARTBEAT.md only if tick-style watching is genuinely wanted.
8. If the current home inventory shows skills, tools, agents, or routines, the definition uses or acknowledges them.
9. The worker can complete an ordinary request with exactly what the inventory shows today. Any "a person must install or approve X before requests can complete" precondition is a defect: report it as misused (tools, skills, or work_layout) and recommend the working path without X, with X as an optional follow-up.
10. Size and tone: an AGENTS.md over 6 KiB or a GOAL.md over 3 KiB is a finding; so is prose that restates the platform contract or piles up refusal conditions. Recommend what to cut.
Report platformFit for every feature in the catalogue that you assessed. Use not_needed when the job does not call for the feature.`,
	"evidence": `You are the evidence and acceptance architect. Review only how done becomes observable. Check every item:
1. GOAL.md states a definition of done a person could verify from files under work/ alone.
2. AGENTS.md tells the worker exactly where RESULT.md goes and what it contains (summary line, result, what was left undone and why), and names every other deliverable path.
3. Each proposed CHECKS.json entry is stable across requests, uses a literal path relative to work/ (with {request_id} only for the current request directory), and is clearly implied by the definition. Reject brittle filenames, dated paths, exact wording that will drift, arbitrary byte counts, and anything that duplicates the built-in RESULT.md check.
4. Whether a per-request check command would be more appropriate for one-off conditions than a worker-level check.
5. The definition never claims a mechanical check proves truth, taste, freshness, business quality, or that an external effect occurred.
6. The escalation path writes the missing item to RESULT.md and stops, so an unfinished run still leaves evidence.
7. Budgets are finite: sources consulted, attempts, output size, time.
8. Every checked path is a file the worker itself writes during a request, or one that already exists in the home. A check on a person-supplied file (a vendored library, license, approval record, tool) fails every request forever: report it as misused under structured_checks and have it removed.
9. Escalation conditions are about the request (ambiguity, conflicting sources, missing authority), never about missing optional assets; a worker must not be told to refuse ordinary work until someone installs something.
Report platformFit for result, structured_checks, request_check, work_layout, escalation, and budgets at minimum.`,
	"authority": `You are the authority and reliability reviewer. Review only the boundaries and failure behavior. Check every item:
1. Network is proposed only when the job itself must read or call a remote service; note the exact remote sources when it is.
2. Every external effect (sending, posting, paying, deleting, changing anything outside the home) is expressed as a work/actions proposal for a person to run through agent act, never as something the worker does.
3. No credential, token, account identifier, or "access already granted" claim appears anywhere in the definition.
4. Unfinished (exit 2), broken check (exit 1), and unknown attempts are anticipated: the definition tells the worker to leave a usable RESULT.md and state so a retry with the same checkpoint continues sensibly.
5. Idempotence: a retried request or a repeated routine occurrence does not duplicate a deliverable or an effect.
6. Definition changes the worker wants go to work/proposals as a unified diff for agent amend, never a direct edit.
7. Human escalation names who decides and when the worker must stop, especially for ambiguity, missing authority, and conflicting sources.
8. Do not invent approval ceremonies. A network grant on the worker record may be used for what the job needs, including fetching a pinned library the person asked for. Supply-chain controls (digests, approval records, validators) are the person's call and belong in the message as an optional follow-up, never as a gate on ordinary work.
Report platformFit for network, actions, proposals, checkpoints, and escalation at minimum.`,
}

type routineSummary struct {
	Title        string `json:"title"`
	Instructions string `json:"instructions"`
	Every        string `json:"every"`
	At           string `json:"at,omitempty"`
	Weekday      int    `json:"weekday"`
	Check        string `json:"check,omitempty"`
	Enabled      bool   `json:"enabled"`
}

// homeInventory is the live state of one existing worker home, gathered with
// model-free commands and directory reads.
type homeInventory struct {
	Slug         string           `json:"slug"`
	Model        string           `json:"model"`
	Network      bool             `json:"network"`
	CheckState   string           `json:"checkState"`
	Show         string           `json:"agentShow"`
	Check        string           `json:"binCheck"`
	Skills       []string         `json:"skills"`
	Tools        []string         `json:"tools"`
	Agents       []string         `json:"agents"`
	StateKeys    []string         `json:"stateKeys"`
	Routines     []routineSummary `json:"routines"`
	RequestCount int              `json:"requestCount"`
}

// platformDossier is what every reviewer and the lead receive about the
// platform on each turn.
type platformDossier struct {
	Suite         map[string]string `json:"suite"`
	AgentContract string            `json:"agentContract"`
	HireContract  string            `json:"hireContract"`
	CheckTemplate string            `json:"compiledCheck"`
	Features      []benchFeature    `json:"features"`
	Home          *homeInventory    `json:"home,omitempty"`
}

func listDirectNames(dir string, limit int) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{}
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		names = append(names, name)
		if len(names) == limit {
			break
		}
	}
	sort.Strings(names)
	return names
}

func (a *application) suiteVersions(ctx context.Context) map[string]string {
	report := a.inspectRuntime(ctx)
	versions := map[string]string{}
	for _, tool := range report.Tools {
		if tool.OK {
			versions[tool.Name] = tool.Version
		} else {
			versions[tool.Name] = "unavailable: " + tool.Message
		}
	}
	return versions
}

// platformDossier assembles the platform knowledge for one builder turn. For
// an existing worker it adds the live inventory: the exact agent show output,
// the compiled check, installed skills/tools/specialists, state keys, and
// routines. Nothing here calls a model.
func (a *application) platformDossier(ctx context.Context, worker Worker, base AgentDefinition) platformDossier {
	dossier := platformDossier{
		Suite:         a.suiteVersions(ctx),
		AgentContract: agentContract,
		HireContract:  hireContract,
		CheckTemplate: renderCheckScript(base.Checks),
		Features:      benchFeatureCatalogue,
	}
	if worker.Slug == "" {
		return dossier
	}
	home := a.homeDir(worker.Slug)
	inventory := &homeInventory{
		Slug:       worker.Slug,
		Model:      worker.Model,
		Network:    worker.Network,
		CheckState: worker.CheckState,
		Skills:     listDirectNames(filepath.Join(home, "skills"), 64),
		Tools:      listDirectNames(filepath.Join(home, "tools"), 128),
		Agents:     listDirectNames(filepath.Join(home, "agents"), 32),
		StateKeys:  listDirectNames(filepath.Join(home, "state", "kv"), 64),
		Routines:   []routineSummary{},
	}
	stdout, stderr, code, err := a.tools.run(ctx, "agent", []string{"show", home}, "", nil, 30*time.Second)
	if err != nil || code != 0 {
		inventory.Show = "agent show failed: " + firstLine(stderr, err)
	} else {
		inventory.Show = promptExcerpt(string(stdout), 16*1024)
	}
	if check, readErr := os.ReadFile(filepath.Join(home, "bin", "check")); readErr == nil {
		inventory.Check = promptExcerpt(string(check), 16*1024)
	}
	if routines, readErr := a.store.Routines(worker.Slug); readErr == nil {
		for _, routine := range routines {
			inventory.Routines = append(inventory.Routines, routineSummary{
				Title: routine.Title, Instructions: promptExcerpt(routine.Instructions, 4000), Every: routine.Every,
				At: routine.At, Weekday: routine.Weekday, Check: routine.Check, Enabled: routine.Enabled,
			})
		}
	}
	if requests, readErr := a.store.Requests(worker.Slug); readErr == nil {
		inventory.RequestCount = len(requests)
	}
	dossier.Home = inventory
	return dossier
}

// builderExpertSchema is generated from the feature catalogue so the closed
// vocabulary the model may use and the one Hire validates cannot drift.
func builderExpertSchema() string {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"summary", "recommendations", "risks", "questions", "platformFit"},
		"properties": map[string]any{
			"summary":         map[string]any{"type": "string", "description": "Two to five sentences: the verdict from this reviewer's focus."},
			"recommendations": map[string]any{"type": "array", "maxItems": 8, "items": map[string]any{"type": "string"}, "description": "Specific changes the lead builder should make to the definition."},
			"risks":           map[string]any{"type": "array", "maxItems": 8, "items": map[string]any{"type": "string"}, "description": "Concrete failure modes that remain if nothing changes."},
			"questions":       map[string]any{"type": "array", "maxItems": 4, "items": map[string]any{"type": "string"}, "description": "Questions only the person can answer; empty when none is needed."},
			"platformFit": map[string]any{
				"type":        "array",
				"maxItems":    len(benchFeatureCatalogue),
				"description": "How the definition uses each Bench feature you assessed. Platform reviewers must report every feature they checked; task experts may leave this empty.",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"feature", "status", "note"},
					"properties": map[string]any{
						"feature": map[string]any{"type": "string", "enum": benchFeatureIDs},
						"status":  map[string]any{"type": "string", "enum": platformFitStatuses, "description": "used: present and correct; missing: the job needs it and the definition lacks it; misused: present but wrong for the contract; not_needed: the job does not call for it."},
						"note":    map[string]any{"type": "string", "description": "One sentence of evidence."},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(schema, "", "  ")
	return string(data)
}
