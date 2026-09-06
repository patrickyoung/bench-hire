package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

type toolReport struct {
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"`
	Version  string `json:"version,omitempty"`
	OK       bool   `json:"ok"`
	Message  string `json:"message,omitempty"`
	Purpose  string `json:"purpose"`
	Required bool   `json:"required"`
}

type provedModelSummary struct {
	Model string    `json:"model"`
	At    time.Time `json:"at"`
}

type modelReport struct {
	Model      string               `json:"model"`
	Provider   string               `json:"provider,omitempty"`
	State      string               `json:"state"`
	Message    string               `json:"message"`
	NextAction string               `json:"nextAction,omitempty"`
	ProvedAt   *time.Time           `json:"provedAt,omitempty"`
	Proved     []provedModelSummary `json:"proved,omitempty"`
}

type RuntimeReport struct {
	Ready             bool         `json:"ready"`
	Summary           string       `json:"summary"`
	Tools             []toolReport `json:"tools"`
	Cage              string       `json:"cage"`
	Model             modelReport  `json:"model"`
	DataRoot          string       `json:"dataRoot"`
	Executable        string       `json:"executable"`
	Jobs              string       `json:"jobs"`
	Location          string       `json:"location"`
	CheckedAt         time.Time    `json:"checkedAt"`
	PassedEnvironment []string     `json:"passedEnvironment"`
}

// inspectRuntime reports what is actually installed. It reads versions and
// cage status; it never spends a model call or turns presence into readiness.
// The probe result is kept for thirty seconds so page polling does not turn
// into a stream of version checks; the model section is always fresh.
func (a *application) inspectRuntime(ctx context.Context) RuntimeReport {
	a.runtimeMu.Lock()
	cached := a.runtimeCache
	fresh := a.runtimeCachedAt
	a.runtimeMu.Unlock()
	if !fresh.IsZero() && a.now().Sub(fresh) < 30*time.Second {
		cached.Model = a.modelReadiness()
		cached.Summary = runtimeSummary(cached.Ready, cached.Model)
		return cached
	}
	report := a.probeRuntime(ctx)
	a.runtimeMu.Lock()
	a.runtimeCache = report
	a.runtimeCachedAt = a.now()
	a.runtimeMu.Unlock()
	return report
}

func runtimeSummary(ready bool, model modelReport) string {
	switch {
	case !ready:
		return "Suite incomplete"
	case model.State == "ready", model.State == "unproved":
		return "Ready to run workers"
	default:
		return "Suite ready · model needs setup"
	}
}

func (a *application) probeRuntime(ctx context.Context) RuntimeReport {
	report := RuntimeReport{DataRoot: a.dataRoot, Executable: a.executable, Location: a.location.String(), CheckedAt: a.now(), PassedEnvironment: append([]string(nil), passedEnvironment...)}
	allOK := true
	for i, name := range append(append([]string{}, requiredTools...), companionTools...) {
		tool := toolReport{Name: name, Path: a.tools.path(name), Purpose: toolPurpose[name], Required: i < len(requiredTools)}
		if tool.Path == "" {
			tool.Message = "not found on PATH"
			if tool.Required {
				allOK = false
			}
		} else {
			stdout, stderr, code, err := a.tools.run(ctx, name, []string{"version"}, "", nil, 5*time.Second)
			if err != nil || code != 0 {
				tool.Message = "version check failed: " + firstLine(stderr, err)
				if tool.Required {
					allOK = false
				}
			} else {
				tool.OK = true
				tool.Version = strings.TrimSpace(string(stdout))
			}
		}
		report.Tools = append(report.Tools, tool)
	}
	if a.tools.path("cage") != "" {
		stdout, _, _, _ := a.tools.run(ctx, "cage", []string{"status"}, "", nil, 5*time.Second)
		report.Cage = strings.TrimSpace(string(stdout))
	}
	if status, err := a.jobs.Check(ctx); err != nil {
		report.Jobs = "tend check failed: " + err.Error()
		allOK = false
	} else {
		report.Jobs = status
	}
	report.Model = a.modelReadiness()
	report.Ready = allOK
	report.Summary = runtimeSummary(allOK, report.Model)
	return report
}

func (a *application) modelReadiness() modelReport { return a.modelReadinessFor(a.defaultModel()) }

// modelReadinessFor judges one model. Readiness is a recorded successful ask
// call for exactly that model, never configuration presence.
func (a *application) modelReadinessFor(model string) modelReport {
	report := modelReport{Model: model, Proved: a.provedModels()}
	if model == "" {
		report.State = "missing"
		report.Message = "No model is configured. Workers can be created and their homes checked, but a run cannot start."
		report.NextAction = "Set a provider/model in Setup, or start Hire with HIRE_MODEL=provider/model."
		return report
	}
	provider, _, ok := strings.Cut(model, "/")
	if !ok {
		report.State = "invalid"
		report.Message = "A model is provider/model, for example anthropic/your-model."
		return report
	}
	report.Provider = provider
	proof, proved, _ := a.store.ProofFor(model)
	if proved && proof.OK {
		at := proof.At
		report.ProvedAt = &at
		report.State = "ready"
		report.Message = "One real ask call answered through this model on " + at.Format("Jan 2 15:04") + "."
		return report
	}
	if provider == "openai-codex" {
		if profile := os.Getenv("HIRE_OAUTH_PROFILE"); profile != "" {
			report.State = "unproved"
			report.Message = "Every ask call for this model runs as oauth with " + profile + " -- ask -header-fd 3."
			report.NextAction = ""
			return report
		}
		status := inspectCodex(a.codexCache)
		if !status.Available {
			report.State = "unconfigured"
			report.Message = status.Message
			report.NextAction = "Run codex login in a terminal (the official Codex CLI); Hire tests the model on its own afterwards."
			return report
		}
		report.State = "unproved"
		report.Message = status.Message + " Hire confirms the model with one small ask call the first time it is needed; the token reaches ask on descriptor 3 only."
		if proved && !proof.OK {
			report.Message = "The last test call failed: " + firstLineText(proof.Output) + ". " + status.Message
			report.NextAction = "Fix the login or the model name, then try again; Setup can run the test call on demand."
		}
		return report
	}
	report.State = "unproved"
	report.Message = "Model selected. Hire tests it through Ask on first use; Ask checks provider support and authentication."
	if proved && !proof.OK {
		report.Message = "The last test call failed: " + firstLineText(proof.Output)
		report.NextAction = "Fix the credentials or the model name, then try again; Setup can run the test call on demand."
	}
	return report
}

// provedModels lists every model with a successful proof on record, so the
// pages can offer them wherever a different model is asked for.
func (a *application) provedModels() []provedModelSummary {
	proofs, err := a.store.ModelProofs()
	if err != nil {
		return nil
	}
	var out []provedModelSummary
	for name, proof := range proofs {
		if proof.OK {
			out = append(out, provedModelSummary{Model: name, At: proof.At})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

func firstLineText(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.Index(text, "\n"); i >= 0 {
		text = text[:i]
	}
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}

// proveModel spends exactly one bounded ask call and records what happened,
// so readiness is evidence rather than configuration presence. An empty model
// means the default; a worker's own model can be proved without changing it.
func (a *application) proveModel(ctx context.Context, model string) (ModelProof, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = a.defaultModel()
	}
	if model == "" {
		return ModelProof{}, fmt.Errorf("no model is configured")
	}
	if err := validateModel(model); err != nil {
		return ModelProof{}, err
	}
	if err := os.MkdirAll(a.askDir, 0o700); err != nil {
		return ModelProof{}, err
	}
	session := a.askDir + "/" + newRequestID("proof", a.now()) + ".jsonl"
	args := []string{"-q", "-m", model, "-f", session, "Reply with exactly the word: ok"}
	recordBuilderCall(ctx)
	stdout, stderr, code, err := a.tools.run(ctx, "ask", args, a.askDir, nil, 90*time.Second)
	proof := ModelProof{Model: model, At: a.now(), Command: "ask -q -m " + model + " -f " + session + " 'Reply with exactly the word: ok'"}
	if err != nil || code != 0 {
		proof.Output = "exit " + fmt.Sprint(code) + ": " + firstLine(stderr, err)
	} else {
		answer := strings.TrimSpace(string(stdout))
		proof.Output = answer
		proof.OK = strings.EqualFold(answer, "ok")
		if !proof.OK {
			proof.Output = "unexpected answer: " + firstLineText(answer)
		}
	}
	if err := a.store.SaveModelProof(proof); err != nil {
		return ModelProof{}, err
	}
	return proof, nil
}
