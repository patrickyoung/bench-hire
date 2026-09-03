package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type intakeRequest struct {
	Text      string `json:"text"`
	Title     string `json:"title"`
	Check     string `json:"check"`
	NotBefore string `json:"notBefore"`
	Plan      bool   `json:"plan"`
}

type intakeResponse struct {
	Request  Request   `json:"request"`
	Plan     *Plan     `json:"plan,omitempty"`
	Actions  []Request `json:"actions"`
	Routines []Routine `json:"routines"`
	Warnings []string  `json:"warnings,omitempty"`
}

const planSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["actions"],
  "properties": {
    "actions": {
      "type": "array",
      "minItems": 1,
      "maxItems": 12,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["title", "instructions", "when", "repeat"],
        "properties": {
          "title": {"type": "string", "description": "Short imperative title, at most 80 characters."},
          "instructions": {"type": "string", "description": "Complete, self-contained instructions for this one action."},
          "when": {"type": "string", "description": "\"now\" or an RFC3339 timestamp with the local offset."},
          "repeat": {"type": "string", "enum": ["", "hourly", "daily", "weekdays", "weekly"], "description": "Empty for a one-time action; otherwise the action recurs and \"when\" is its first occurrence."}
        }
      }
    }
  }
}
`

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())[:n*2]
	}
	return hex.EncodeToString(buf)
}

func newRequestID(prefix string, now time.Time) string {
	return prefix + "-" + now.UTC().Format("20060102-150405") + "-" + randomHex(2)
}

// renderRequestFile is the exact REQUEST.md the worker and its check read.
// Single-line header fields keep the check's sed extraction unambiguous.
func renderRequestFile(r Request) string {
	var b strings.Builder
	b.WriteString("id: " + r.ID + "\n")
	b.WriteString("kind: " + r.Kind + "\n")
	b.WriteString("title: " + oneLine(r.Title) + "\n")
	b.WriteString("check: " + oneLine(r.Check) + "\n")
	b.WriteString("result: work/requests/" + r.ID + "/RESULT.md\n")
	b.WriteString("created: " + r.CreatedAt.UTC().Format(time.RFC3339) + "\n")
	b.WriteString("\n# Request\n\n")
	b.WriteString(strings.TrimSpace(r.Text))
	b.WriteString("\n")
	return b.String()
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

func focusText(r Request) string {
	return fmt.Sprintf("Handle request %s: %s. Read REQUEST.md at the home root for the full request, do the work, and write work/requests/%s/RESULT.md.", r.ID, oneLine(r.Title), r.ID)
}

// fallbackPlan is the model-free plan: one action, now, the whole text.
func fallbackPlan(text string, reason string) Plan {
	return Plan{Fallback: true, Error: reason, Actions: []PlannedAction{{Title: summaryLine(text), Instructions: strings.TrimSpace(text), When: "now"}}}
}

func planSystemPrompt(w Worker, now time.Time, loc *time.Location) string {
	return fmt.Sprintf(`You turn one request for a digital worker into a short list of concrete actions.
The worker is %q. Its job: %s
The current local time is %s (%s).

Rules:
- If the request asks for one thing with no timing, return exactly one action with when "now".
- Use "now" for anything that should start immediately. Use an RFC3339 timestamp with the local offset for a later time.
- Use repeat only when the request clearly asks for something recurring; then when is the first occurrence.
- Each action's instructions must be self-contained. The worker sees only that action's instructions, never the original request or the other actions.
- Keep the person's words and specifics. Do not add work that was not asked for, and do not split one coherent task into fragments.
- Reply with JSON matching the schema and nothing else.`, w.Name, strings.TrimSpace(w.Purpose), now.In(loc).Format(time.RFC3339), loc.String())
}

// planWithModel asks once, schema-bound, in a fresh session under var/ask so a
// scheduled call never continues a terminal conversation.
func (a *application) planWithModel(ctx context.Context, w Worker, text string, now time.Time) (Plan, error) {
	model := w.Model
	if model == "" {
		return Plan{}, errors.New("no model configured")
	}
	if err := os.MkdirAll(a.askDir, 0o700); err != nil {
		return Plan{}, err
	}
	schemaPath := filepath.Join(a.askDir, "plan-schema.json")
	if err := writeFileAtomic(schemaPath, []byte(planSchema), false); err != nil {
		return Plan{}, err
	}
	sessionPath := filepath.Join(a.askDir, "plan-"+now.UTC().Format("20060102-150405")+"-"+randomHex(3)+".jsonl")
	args := []string{"-q", "-m", model, "-f", sessionPath, "-schema", schemaPath, "-S", planSystemPrompt(w, now, a.location), "Request:\n\n" + strings.TrimSpace(text)}
	stdout, stderr, code, err := a.tools.run(ctx, "ask", args, a.askDir, nil, 3*time.Minute)
	if err != nil || code != 0 {
		return Plan{}, fmt.Errorf("ask exited %d: %s", code, firstLine(stderr, err))
	}
	var parsed struct {
		Actions []PlannedAction `json:"actions"`
	}
	raw := []byte(strings.TrimSpace(string(stdout)))
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Plan{}, fmt.Errorf("planner returned something other than the schema: %v", err)
	}
	if len(parsed.Actions) == 0 {
		return Plan{}, errors.New("planner returned no actions")
	}
	for i := range parsed.Actions {
		act := &parsed.Actions[i]
		act.Title = summaryLine(strings.TrimSpace(act.Title + "\n"))
		if strings.TrimSpace(act.Instructions) == "" {
			return Plan{}, errors.New("planner returned an action without instructions")
		}
		if _, ok := namedCadences[act.Repeat]; !ok && act.Repeat != "" {
			act.Repeat = ""
		}
		if act.When != "now" {
			if _, err := time.Parse(time.RFC3339, act.When); err != nil {
				act.When = "now"
			}
		}
	}
	return Plan{Model: model, Raw: json.RawMessage(raw), Actions: parsed.Actions, Session: sessionPath}, nil
}

// intake records what arrived, decides the actions, and queues each one.
func (a *application) intake(ctx context.Context, w Worker, in intakeRequest) (intakeResponse, error) {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return intakeResponse{}, errors.New("a request needs some text")
	}
	if len(text) > 64*1024 {
		return intakeResponse{}, errors.New("a request is limited to 64 KiB")
	}
	if !w.Enabled || w.CheckState != "valid" {
		return intakeResponse{}, errors.New("this worker is not accepting work until its home passes agent check and it is enabled")
	}
	now := a.now()
	notBefore := now
	if strings.TrimSpace(in.NotBefore) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(in.NotBefore))
		if err != nil {
			return intakeResponse{}, errors.New("notBefore must be an RFC3339 time")
		}
		if parsed.After(now) {
			notBefore = parsed
		}
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = summaryLine(text)
	}
	parent := Request{ID: newRequestID("req", now), WorkerSlug: w.Slug, Kind: "request", Title: title, Text: text, Check: strings.TrimSpace(in.Check), Source: "user", NotBefore: notBefore, Model: w.Model, Network: w.Network, CreatedAt: now}
	response := intakeResponse{Request: parent}
	var plan Plan
	if in.Plan {
		planned, err := a.planWithModel(ctx, w, text, now)
		if err != nil {
			plan = fallbackPlan(text, err.Error())
			response.Warnings = append(response.Warnings, "The planner was not available ("+err.Error()+"); the request runs as one action now.")
		} else {
			plan = planned
		}
	} else {
		plan = fallbackPlan(text, "")
	}
	plan.RequestID = parent.ID
	plan.CreatedAt = now
	single := len(plan.Actions) == 1 && plan.Actions[0].Repeat == "" && plan.Actions[0].When == "now"
	if single {
		// One action now is the request itself: no child records, one job.
		parent.Runs = true
		parent.Text = plan.Actions[0].Instructions
		if !plan.Fallback {
			parent.Title = plan.Actions[0].Title
		}
		if err := a.store.CreateRequest(parent); err != nil {
			return intakeResponse{}, err
		}
		if err := a.store.SavePlan(w.Slug, plan); err != nil {
			return intakeResponse{}, err
		}
		if err := a.submitRequest(ctx, parent); err != nil {
			return intakeResponse{}, err
		}
		response.Request = parent
		response.Plan = &plan
		response.Actions = []Request{parent}
		return response, nil
	}
	if err := a.store.CreateRequest(parent); err != nil {
		return intakeResponse{}, err
	}
	for i := range plan.Actions {
		act := &plan.Actions[i]
		when := notBefore
		if act.When != "now" {
			if parsed, err := time.Parse(time.RFC3339, act.When); err == nil && parsed.After(when) {
				when = parsed
			}
		}
		if act.Repeat != "" {
			at := when.In(a.location).Format("15:04")
			r, err := a.buildRoutine(w.Slug, routineInput{Title: act.Title, Instructions: act.Instructions, Every: act.Repeat, At: at, Weekday: int(when.In(a.location).Weekday()), Check: parent.Check}, "plan:"+parent.ID, now)
			if err != nil {
				response.Warnings = append(response.Warnings, "Could not schedule "+act.Title+": "+err.Error())
				continue
			}
			if when.After(now) {
				r.NextDue = when
			}
			if err := a.store.SaveRoutine(r); err != nil {
				return intakeResponse{}, err
			}
			act.RoutineID = r.ID
			response.Routines = append(response.Routines, r)
			continue
		}
		child := Request{ID: fmt.Sprintf("%s-a%d", parent.ID, i+1), WorkerSlug: w.Slug, Kind: "action", Title: act.Title, Text: act.Instructions, Check: parent.Check, Source: "plan:" + parent.ID, ParentID: parent.ID, NotBefore: when, Model: w.Model, Network: w.Network, Runs: true, CreatedAt: now}
		if err := a.store.CreateRequest(child); err != nil {
			return intakeResponse{}, err
		}
		if err := a.submitRequest(ctx, child); err != nil {
			return intakeResponse{}, err
		}
		act.RequestID = child.ID
		response.Actions = append(response.Actions, child)
	}
	if err := a.store.SavePlan(w.Slug, plan); err != nil {
		return intakeResponse{}, err
	}
	response.Plan = &plan
	return response, nil
}

// submitRequest hands Tend the exact argv that re-runs this request. The job
// id is the request id, so a second submission of the same request is a
// no-op rather than a duplicate.
func (a *application) submitRequest(ctx context.Context, r Request) error {
	home := a.homeDir(r.WorkerSlug)
	argv := []string{a.executable, "exec", home, a.store.RequestPath(r.WorkerSlug, r.ID)}
	return a.jobs.Submit(ctx, r.ID, home, r.NotBefore, argv)
}

// queueRoutine turns one due occurrence into a request and a job.
func (a *application) queueRoutine(ctx context.Context, w Worker, r Routine, due time.Time, now time.Time) (Request, error) {
	req := Request{ID: occurrenceID(r.ID, due), WorkerSlug: w.Slug, Kind: "routine", Title: r.Title, Text: r.Instructions, Check: r.Check, Source: "routine:" + r.ID, NotBefore: due, Model: w.Model, Network: w.Network, Runs: true, CreatedAt: now}
	if _, err := a.store.Request(w.Slug, req.ID); err == nil {
		return req, nil
	}
	if err := a.store.CreateRequest(req); err != nil {
		return Request{}, err
	}
	if err := a.submitRequest(ctx, req); err != nil {
		return Request{}, err
	}
	return req, nil
}
