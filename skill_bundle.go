package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const skillBundleLimit = 128 << 20
const skillBundleFileLimit = 16 << 20
const skillBundleTextLimit = 8 << 20
const skillBundleEntries = 2048

type SkillBundleFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
	Dir    bool   `json:"dir,omitempty"`
	Text   bool   `json:"text,omitempty"`
}

type SkillBundle struct {
	RootMode uint32            `json:"rootMode"`
	SHA256   string            `json:"sha256"`
	Files    []SkillBundleFile `json:"files"`
}

func skillBundlePath(path string) bool {
	if path == "" || len(path) > 512 || strings.ContainsAny(path, "\\:") || strings.HasPrefix(path, "/") {
		return false
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return false
		}
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// A bundle manifest includes every file, empty directory and permission mode.
// Unsupported links/special files and oversized bundles are explicit errors;
// they are never silently dropped from an improvement or its rollback copy.
func readSkillBundle(root string) (SkillBundle, error) {
	bundle := SkillBundle{Files: []SkillBundleFile{}}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return bundle, errors.New("the skill must be an ordinary directory")
	}
	var total int64
	bundle.RootMode = uint32(info.Mode().Perm())
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || !skillBundlePath(filepath.ToSlash(rel)) {
			return errors.New("skill contains an unsupported file path")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if len(bundle.Files) >= skillBundleEntries {
			return fmt.Errorf("skill exceeds %d entries; split the bundle before improving it", skillBundleEntries)
		}
		file := SkillBundleFile{Path: filepath.ToSlash(rel), Mode: uint32(info.Mode().Perm()), Dir: info.IsDir()}
		if !info.IsDir() {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s is a link or special file; only ordinary files and directories can be revised", rel)
			}
			raw, err := readRegularFileLimit(path, skillBundleFileLimit)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			file.Size, file.SHA256 = int64(len(raw)), contentSHA256(raw)
			file.Text = utf8.Valid(raw) && !bytes.ContainsRune(raw, '\x00')
			total += file.Size
			if total > skillBundleLimit {
				return errors.New("skill exceeds the 128 MiB bundle limit")
			}
		}
		bundle.Files = append(bundle.Files, file)
		return nil
	})
	if err != nil {
		return bundle, err
	}
	slices.SortFunc(bundle.Files, func(a, b SkillBundleFile) int { return strings.Compare(a.Path, b.Path) })
	raw, _ := json.Marshal(struct {
		Mode  uint32
		Files []SkillBundleFile
	}{bundle.RootMode, bundle.Files})
	bundle.SHA256 = contentSHA256(raw)
	return bundle, nil
}

func verifySkillBundle(root string, expected SkillBundle) error {
	actual, err := readSkillBundle(root)
	if err != nil {
		return err
	}
	if actual.SHA256 != expected.SHA256 {
		return errors.New("skill files changed after their snapshot; reopen the skill before continuing")
	}
	return nil
}

func copySkillBundle(source, target string, expected SkillBundle) error {
	if err := verifySkillBundle(source, expected); err != nil {
		return err
	}
	if err := os.Mkdir(target, 0700); err != nil {
		return err
	}
	for _, file := range expected.Files {
		path := filepath.Join(target, filepath.FromSlash(file.Path))
		if file.Dir {
			if err := os.MkdirAll(path, 0700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		raw, err := readRegularFileLimit(filepath.Join(source, filepath.FromSlash(file.Path)), skillBundleFileLimit)
		if err != nil || contentSHA256(raw) != file.SHA256 {
			return errors.New("source skill changed while copying")
		}
		if err := writeFileAtomic(path, raw, true); err != nil {
			return err
		}
		if err := os.Chmod(path, fs.FileMode(file.Mode)); err != nil {
			return err
		}
	}
	// Restore directory permissions after all children have been written.
	for i := len(expected.Files) - 1; i >= 0; i-- {
		file := expected.Files[i]
		if file.Dir {
			if err := os.Chmod(filepath.Join(target, filepath.FromSlash(file.Path)), fs.FileMode(file.Mode)); err != nil {
				return err
			}
		}
	}
	if err := os.Chmod(target, fs.FileMode(expected.RootMode)); err != nil {
		return err
	}
	return verifySkillBundle(target, expected)
}

type SkillFileChange struct {
	SourcePath string `json:"sourcePath"`
	Path       string `json:"path"`
	Operation  string `json:"operation"`
	Content    string `json:"content"`
	UploadID   string `json:"uploadID"`
	Executable bool   `json:"executable"`
	Reason     string `json:"reason"`
}

type SkillBundleDelta struct {
	Path   string           `json:"path"`
	Kind   string           `json:"kind"`
	Before *SkillBundleFile `json:"before,omitempty"`
	After  *SkillBundleFile `json:"after,omitempty"`
}

func skillBundleChanges(before, after SkillBundle) []SkillBundleDelta {
	old, fresh := map[string]SkillBundleFile{}, map[string]SkillBundleFile{}
	paths := []string{}
	for _, file := range before.Files {
		old[file.Path] = file
		paths = append(paths, file.Path)
	}
	for _, file := range after.Files {
		fresh[file.Path] = file
		if _, ok := old[file.Path]; !ok {
			paths = append(paths, file.Path)
		}
	}
	slices.Sort(paths)
	changes := []SkillBundleDelta{}
	for _, path := range paths {
		left, hasLeft := old[path]
		right, hasRight := fresh[path]
		if hasLeft && hasRight && left == right {
			continue
		}
		change := SkillBundleDelta{Path: path, Kind: "updated"}
		if hasLeft {
			change.Before = &left
		} else {
			change.Kind = "added"
		}
		if hasRight {
			change.After = &right
		} else {
			change.Kind = "removed"
		}
		changes = append(changes, change)
	}
	return changes
}
