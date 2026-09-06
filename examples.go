package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

//go:embed examples/*
var exampleAssets embed.FS

type workerExample struct {
	ID         string          `json:"id"`
	Summary    string          `json:"summary"`
	CheckScope string          `json:"checkScope"`
	Definition AgentDefinition `json:"definition"`
	Task       intakeRequest   `json:"task"`
	Files      []string        `json:"files"`
	Recovery   string          `json:"recovery"`
}

func loadExample(id string) (workerExample, error) {
	if id != "reporting" && id != "documents" && id != "maintenance" {
		return workerExample{}, errNotFound
	}
	root := "examples/" + id + "/"
	var example workerExample
	data, err := exampleAssets.ReadFile(root + "example.json")
	if err != nil {
		return example, err
	}
	if err := json.Unmarshal(data, &example); err != nil {
		return example, err
	}
	example.ID = id
	for name, target := range map[string]*string{
		"GOAL.md":     &example.Definition.Files.Goal,
		"AGENTS.md":   &example.Definition.Files.Agents,
		"TASK.md":     &example.Task.Text,
		"RECOVERY.md": &example.Recovery,
	} {
		data, err := exampleAssets.ReadFile(root + name)
		if err != nil {
			return example, err
		}
		*target = string(data)
	}
	example.Definition, err = normalizeAgentDefinition(example.Definition, true)
	if err != nil {
		return example, err
	}
	err = fs.WalkDir(exampleAssets, root+"home", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(path, root+"home/")
		if !strings.HasPrefix(rel, "tools/") && !strings.HasPrefix(rel, "work/") && !strings.HasPrefix(rel, "inputs/") {
			return fmt.Errorf("unexpected example file %s", rel)
		}
		example.Files = append(example.Files, rel)
		return nil
	})
	return example, err
}

// Install before publishing worker.json. A task cannot be accepted until the
// complete definition, inputs and check are present and Agent has checked them.
func installExample(home string, example workerExample) error {
	if err := restoreAgentDefinition(home, example.Definition); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(home, "inputs"), 0o755); err != nil {
		return err
	}
	for _, rel := range example.Files {
		data, err := exampleAssets.ReadFile("examples/" + example.ID + "/home/" + rel)
		if err != nil {
			return err
		}
		path, err := withinHome(home, rel)
		if err != nil {
			return err
		}
		if err := writeFileAtomic(path, data, true); err != nil {
			return err
		}
		if strings.HasPrefix(rel, "tools/") {
			if err := os.Chmod(path, 0o755); err != nil {
				return err
			}
		}
	}
	return writeFileAtomic(filepath.Join(home, "work", "EXAMPLE.md"), []byte(example.Task.Text+"\n## Try a correction\n\n"+example.Recovery), true)
}

func (a *application) handleExamples(w http.ResponseWriter, r *http.Request) {
	var examples []workerExample
	for _, id := range []string{"reporting", "documents", "maintenance"} {
		example, err := loadExample(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "examples", err.Error(), "")
			return
		}
		examples = append(examples, example)
	}
	writeJSON(w, http.StatusOK, map[string]any{"examples": examples})
}
