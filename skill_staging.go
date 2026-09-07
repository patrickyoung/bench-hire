package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

func isSkillText(raw []byte) bool { return utf8.Valid(raw) && !bytes.ContainsRune(raw, '\x00') }

func skillProposalSHA(revision SkillImprovement) string {
	raw, _ := json.Marshal(struct {
		Before, After, Evidence, Harness string
		Plan                             SkillImprovementPlan
	}{revision.Before.SHA256, revision.After.SHA256, revision.EvidenceSHA256, revision.ChecksSHA256, revision.Plan})
	return contentSHA256(raw)
}

func validateSkillPlan(plan SkillImprovementPlan) error {
	raw, _ := json.Marshal(plan)
	if len(raw) > 2<<20 || strings.TrimSpace(plan.Summary) == "" || len(plan.Summary) > 12000 || len(plan.Assumptions) > 16 || len(plan.Changes) < 1 || len(plan.Changes) > 64 || len(plan.CheckFiles) > 32 || len(plan.Checks) < 1 || len(plan.Checks) > 8 {
		return errors.New("the proposal needs a concise explanation, 1–64 explicit file changes and 1–8 executable checks")
	}
	seen := map[string]bool{}
	for _, change := range plan.Changes {
		if !skillBundlePath(change.Path) || seen[change.Path] || strings.TrimSpace(change.Reason) == "" || len(change.Reason) > 4000 {
			return errors.New("each changed file needs a distinct relative path and a reason")
		}
		seen[change.Path] = true
		if change.Operation != "write" && change.Operation != "copy-upload" && change.Operation != "copy-file" && change.Operation != "delete" {
			return errors.New("unsupported skill file operation")
		}
		if change.Operation == "write" && (!isSkillText([]byte(change.Content)) || len(change.Content) > 512<<10) {
			return errors.New("generated skill files must be UTF-8 text under 512 KiB; use a source upload for binary assets")
		}
		if change.Operation != "write" && change.Content != "" {
			return errors.New("only text writes may supply content")
		}
		if change.Operation == "copy-file" && !skillBundlePath(change.SourcePath) {
			return errors.New("choose an existing skill file to copy")
		}
		if change.Path == "SKILL.md" && change.Operation == "delete" {
			return errors.New("a skill must retain SKILL.md")
		}
	}
	seen = map[string]bool{}
	for _, file := range plan.CheckFiles {
		if !skillBundlePath(file.Path) || seen[file.Path] || !isSkillText([]byte(file.Content)) || len(file.Content) > 256<<10 {
			return errors.New("check harness files need distinct relative paths and bounded UTF-8 content")
		}
		seen[file.Path] = true
	}
	for _, check := range plan.Checks {
		if strings.TrimSpace(check.Name) == "" || len(check.Name) > 160 || strings.TrimSpace(check.Purpose) == "" || len(check.Purpose) > 1500 || len(check.Argv) == 0 || len(check.Argv) > 32 {
			return errors.New("each check needs a name, purpose and literal argument array")
		}
		for _, arg := range check.Argv {
			if strings.ContainsRune(arg, '\x00') || len(arg) > 4096 {
				return errors.New("invalid check argument")
			}
		}
		if check.Argv[0] == "" || strings.HasPrefix(check.Argv[0], "-") {
			return errors.New("a check must name its executable")
		}
	}
	return nil
}

func (a *application) stageSkillImprovement(ctx context.Context, revision *SkillImprovement, dir string) error {
	if err := validateSkillPlan(revision.Plan); err != nil {
		return err
	}
	explanation := revision.Plan.Summary
	for _, assumption := range revision.Plan.Assumptions {
		explanation += "\n" + assumption
	}
	if err := a.checkSkillCitations(ctx, dir, *revision, explanation); err != nil {
		return err
	}
	for _, change := range revision.Plan.Changes {
		if err := a.checkSkillCitations(ctx, dir, *revision, change.Reason); err != nil {
			return fmt.Errorf("%s: %w", change.Path, err)
		}
	}
	candidate := filepath.Join(dir, "candidate")
	if err := copySkillBundle(filepath.Join(dir, "before"), candidate, revision.Before); err != nil {
		return err
	}
	original := map[string]SkillBundleFile{}
	for _, file := range revision.Before.Files {
		original[file.Path] = file
	}
	for _, change := range revision.Plan.Changes {
		for _, ref := range skillUploadReferences(revision.Uploads) {
			if change.Path == ref.OriginalPath || change.Path == ref.TextPath || change.Path == ref.EvidencePath {
				return errors.New("managed upload references cannot be changed by the proposal")
			}
		}
		target, err := withinHome(candidate, change.Path)
		if err != nil {
			return err
		}
		var raw []byte
		switch change.Operation {
		case "delete":
			if _, ok := original[change.Path]; !ok {
				return fmt.Errorf("cannot remove missing file %s", change.Path)
			}
			if err := os.Remove(target); err != nil {
				return fmt.Errorf("remove %s: %w", change.Path, err)
			}
			continue
		case "write":
			raw = []byte(change.Content)
		case "copy-file":
			file, ok := original[change.SourcePath]
			if !ok || file.Dir {
				return errors.New("copy source is not an original skill file")
			}
			raw, err = readRegularFileLimit(filepath.Join(dir, "before", filepath.FromSlash(change.SourcePath)), skillBundleFileLimit)
			if err != nil || contentSHA256(raw) != file.SHA256 {
				return errors.New("original file changed before copying")
			}
		case "copy-upload":
			if !slices.Contains(revision.UploadIDs, change.UploadID) {
				return errors.New("copy source must be one of this improvement's uploads")
			}
			for _, ref := range revision.Uploads {
				if ref.ID == change.UploadID {
					raw, err = readRegularFileLimit(filepath.Join(dir, filepath.FromSlash(ref.OriginalPath)), uploadFileLimit)
					if err != nil || contentSHA256(raw) != ref.SHA256 {
						return errors.New("retained upload changed before copying")
					}
				}
			}
			if raw == nil {
				return errors.New("uploaded original is unavailable")
			}
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := writeFileAtomic(target, raw, false); err != nil {
			return err
		}
		mode := fs.FileMode(0644)
		if old, ok := original[change.Path]; ok {
			mode = fs.FileMode(old.Mode) &^ 0111
		}
		if change.Executable {
			mode |= 0111
		}
		if err := os.Chmod(target, mode); err != nil {
			return err
		}
	}
	if err := verifyUploadRefs(dir, revision.Uploads); err != nil {
		return err
	}
	refs := skillUploadReferences(revision.Uploads)
	for i, ref := range refs {
		original := revision.Uploads[i]
		for _, pair := range [][2]string{{original.OriginalPath, ref.OriginalPath}, {original.TextPath, ref.TextPath}, {original.EvidencePath, ref.EvidencePath}} {
			raw, err := readRegularFileLimit(filepath.Join(dir, filepath.FromSlash(pair[0])), skillBundleFileLimit)
			if err != nil {
				return err
			}
			target, err := withinHome(candidate, pair[1])
			if err != nil {
				return err
			}
			if existing, err := readRegularFileLimit(target, skillBundleFileLimit); err == nil {
				if !bytes.Equal(raw, existing) {
					return errors.New("retained skill source has different bytes")
				}
			} else if errors.Is(err, os.ErrNotExist) {
				if err := writeFileAtomic(target, raw, true); err != nil {
					return err
				}
			} else {
				return err
			}
		}
	}
	skillPath := filepath.Join(candidate, "SKILL.md")
	raw, err := readRegularFileLimit(skillPath, 32768)
	if err != nil {
		return err
	}
	if len(refs) > 0 {
		info, err := os.Stat(skillPath)
		if err != nil {
			return err
		}
		raw = append(raw, []byte("\n\n## Materials for this improvement\n\nTaught from supplied materials; usefulness is assessed by the recorded checks and later work.\n"+referenceInstructions(refs))...)
		if err := writeFileAtomic(skillPath, raw, false); err != nil {
			return err
		}
		if err := os.Chmod(skillPath, info.Mode().Perm()); err != nil {
			return err
		}
	}
	if len(raw) > 32768 {
		return errors.New("SKILL.md exceeds 32 KiB including its source instructions; move detail into resources")
	}
	// Catch invented or broken citation tokens in every proposed text file.
	var cited strings.Builder
	cited.WriteString(explanation)
	for _, change := range revision.Plan.Changes {
		cited.WriteString("\n" + change.Content)
	}
	cited.Write(raw)
	if err := a.checkSkillCitations(ctx, dir, *revision, cited.String()); err != nil {
		return err
	}
	checksDir := filepath.Join(dir, "checks")
	if err := os.Mkdir(checksDir, 0700); err != nil {
		return err
	}
	for _, file := range revision.Plan.CheckFiles {
		path := filepath.Join(checksDir, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := writeFileAtomic(path, []byte(file.Content), true); err != nil {
			return err
		}
	}
	harness, err := readSkillBundle(checksDir)
	if err != nil {
		return err
	}
	revision.ChecksSHA256 = harness.SHA256
	revision.After, err = readSkillBundle(candidate)
	if err != nil {
		return err
	}
	if len(skillBundleChanges(revision.Before, revision.After)) == 0 {
		return errors.New("the proposal did not change the skill")
	}
	revision.ProposalSHA256 = skillProposalSHA(*revision)
	// Brief validates the named skill in a catalogue with the correct directory
	// name. Model prose never substitutes for this executable check.
	lint := a.lintSkillCandidate(ctx, dir, *revision, "candidate")
	revision.Results = []SkillCheckResult{lint}
	return nil
}

func (a *application) lintSkillCandidate(ctx context.Context, dir string, revision SkillImprovement, version string) SkillCheckResult {
	result := SkillCheckResult{Name: "Skill structure and resource links", Version: version, State: "broken", Exit: -1}
	start := time.Now()
	parent, err := os.MkdirTemp(dir, "lint-")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer os.RemoveAll(parent)
	expected := revision.After
	if version == "before" {
		expected = revision.Before
	}
	target := filepath.Join(parent, revision.Skill)
	if err = copySkillBundle(filepath.Join(dir, version), target, expected); err == nil {
		var stdout, stderr []byte
		stdout, stderr, result.Exit, err = a.tools.run(ctx, "brief", []string{"lint", "-strict", target}, parent, nil, 30*time.Second)
		result.Output = clippedSkillOutput(append(stdout, stderr...))
	}
	if err != nil {
		result.Error = err.Error()
	} else if result.Exit == 0 {
		result.State = "passed"
	} else if result.Exit == 1 {
		result.State = "failed"
	}
	result.DurationMS = time.Since(start).Milliseconds()
	return result
}

func clippedSkillOutput(raw []byte) string {
	if len(raw) > 64<<10 {
		return string(raw[:64<<10]) + "\n[Output truncated at 64 KiB]"
	}
	return string(raw)
}

// Brief keeps supporting resources one level from SKILL.md. Prefix each
// retained file with its immutable upload ID without changing its bytes.
func skillUploadReferences(uploads []UploadRef) []UploadRef {
	refs := slices.Clone(uploads)
	for i, ref := range refs {
		refs[i].OriginalPath = "references/" + ref.ID + "-" + filepath.Base(ref.OriginalPath)
		refs[i].TextPath = "references/" + ref.ID + "-reference.md"
		refs[i].EvidencePath = "references/" + ref.ID + "-context.jsonl"
	}
	return refs
}
