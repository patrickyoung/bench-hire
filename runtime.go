package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

type toolReport struct {
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

type modelReport struct {
	Model      string     `json:"model"`
	Provider   string     `json:"provider,omitempty"`
	State      string     `json:"state"`
	Message    string     `json:"message"`
	NextAction string     `json:"nextAction,omitempty"`
	ProvedAt   *time.Time `json:"provedAt,omitempty"`
}

type RuntimeReport struct {
	Ready      bool         `json:"ready"`
	Summary    string       `json:"summary"`
	Tools      []toolReport `json:"tools"`
	Cage       string       `json:"cage"`
	Model      modelReport  `json:"model"`
	DataRoot   string       `json:"dataRoot"`
	Executable string       `json:"executable"`
	Jobs       string       `json:"jobs"`
	Location   string       `json:"location"`
	CheckedAt  time.Time    `json:"checkedAt"`
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
	case model.State == "ready":
		return "Ready to run workers"
	default:
		return "Suite ready · model unproved"
	}
}

func (a *application) probeRuntime(ctx context.Context) RuntimeReport {
	report := RuntimeReport{DataRoot: a.dataRoot, Executable: a.executable, Location: a.location.String(), CheckedAt: a.now()}
	allOK := true
	for _, name := range requiredTools {
		tool := toolReport{Name: name, Path: a.tools.path(name)}
		if tool.Path == "" {
			tool.Message = "not found on PATH"
			allOK = false
		} else {
			stdout, stderr, code, err := a.tools.run(ctx, name, []string{"version"}, "", nil, 5*time.Second)
			if err != nil || code != 0 {
				tool.Message = "version check failed: " + firstLine(stderr, err)
				allOK = false
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

var providerKeys = map[string]string{
	"anthropic":  "ANTHROPIC_API_KEY",
	"openai":     "OPENAI_API_KEY",
	"gemini":     "GEMINI_API_KEY",
	"openrouter": "OPENROUTER_API_KEY",
}

func (a *application) modelReadiness() modelReport {
	model := a.defaultModel()
	report := modelReport{Model: model}
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
	proof, proved, _ := a.store.ModelProof()
	if proved && proof.Model == model && proof.OK {
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
			report.NextAction = "Press Prove the model to confirm the profile can answer."
			return report
		}
		status := inspectCodex(a.codexCache)
		if !status.Available {
			report.State = "unconfigured"
			report.Message = status.Message
			report.NextAction = "Run codex login in a terminal (the official Codex CLI), then press Prove the model."
			return report
		}
		report.State = "unproved"
		report.Message = status.Message + " The token reaches ask on descriptor 3 only; it never enters argv, the environment, or a session log."
		report.NextAction = "Press Prove the model to spend one small ask call and record the receipt."
		if proved && proof.Model == model && !proof.OK {
			report.Message = "The last proof attempt failed: " + firstLineText(proof.Output) + ". " + status.Message
		}
		return report
	}
	if key, known := providerKeys[provider]; known {
		if os.Getenv(key) == "" && os.Getenv(strings.ToUpper(provider)+"_BASE_URL") == "" {
			report.State = "unconfigured"
			report.Message = key + " is not set in Hire's environment, so ask cannot reach " + provider + "."
			report.NextAction = "Start Hire with " + key + " exported, then press Prove the model."
			return report
		}
	}
	report.State = "unproved"
	report.Message = "The model is configured but no ask call has proved it yet."
	report.NextAction = "Press Prove the model to spend one small ask call and record the receipt."
	if proved && proof.Model == model && !proof.OK {
		report.Message = "The last proof attempt failed: " + firstLineText(proof.Output)
	}
	return report
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
// so readiness is evidence rather than configuration presence.
func (a *application) proveModel(ctx context.Context) (ModelProof, error) {
	model := a.defaultModel()
	if model == "" {
		return ModelProof{}, fmt.Errorf("no model is configured")
	}
	if err := os.MkdirAll(a.askDir, 0o700); err != nil {
		return ModelProof{}, err
	}
	session := a.askDir + "/proof-" + a.now().UTC().Format("20060102-150405") + ".jsonl"
	args := []string{"-q", "-m", model, "-f", session, "Reply with exactly the word: ok"}
	stdout, stderr, code, err := a.tools.run(ctx, "ask", args, a.askDir, nil, 90*time.Second)
	proof := ModelProof{Model: model, At: a.now(), Command: "ask -q -m " + model + " -f " + session + " 'Reply with exactly the word: ok'"}
	if err != nil || code != 0 {
		proof.Output = "exit " + fmt.Sprint(code) + ": " + firstLine(stderr, err)
	} else {
		answer := strings.TrimSpace(string(stdout))
		proof.Output = answer
		proof.OK = strings.Contains(strings.ToLower(answer), "ok")
		if !proof.OK {
			proof.Output = "unexpected answer: " + firstLineText(answer)
		}
	}
	if err := a.store.SaveModelProof(proof); err != nil {
		return ModelProof{}, err
	}
	return proof, nil
}
