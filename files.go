package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const fileReadLimit = 512 * 1024
const fileWriteLimit = 2 * 1024 * 1024

type fileEntry struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Dir      bool      `json:"dir"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type fileView struct {
	Path      string      `json:"path"`
	Dir       bool        `json:"dir"`
	Entries   []fileEntry `json:"entries,omitempty"`
	Content   string      `json:"content,omitempty"`
	Size      int64       `json:"size"`
	Truncated bool        `json:"truncated"`
	Binary    bool        `json:"binary"`
	Writable  bool        `json:"writable"`
	Modified  time.Time   `json:"modified"`
}

var definitionFiles = []string{"GOAL.md", "AGENTS.md", "SOUL.md", "PLAN.md", "MEMORY.md", "HEARTBEAT.md"}

// browsableRoots are what a person may look at from the web page. Evidence
// under .agent is reached through the history commands, not raw listing.
var browsableRoots = []string{"work", "state", "tools", "skills", "REQUEST.md"}

func isDefinitionFile(name string) bool {
	for _, f := range definitionFiles {
		if f == name {
			return true
		}
	}
	return false
}

// cleanRelative rejects anything that could leave the home: absolute paths,
// parent references, and empty segments.
func cleanRelative(rel string) (string, error) {
	rel = strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	rel = strings.TrimPrefix(rel, "./")
	if rel == "" || rel == "." {
		return "", nil
	}
	if strings.HasPrefix(rel, "/") {
		return "", errors.New("path must be relative to the worker home")
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("path may not contain empty or parent segments")
		}
	}
	return filepath.Clean(rel), nil
}

func isBrowsable(rel string) bool {
	if rel == "" {
		return true
	}
	if isDefinitionFile(rel) {
		return true
	}
	for _, root := range browsableRoots {
		if rel == root || strings.HasPrefix(rel, root+"/") {
			return true
		}
	}
	return false
}

func isWritable(rel string) bool {
	return strings.HasPrefix(rel, "work/") || strings.HasPrefix(rel, "state/")
}

// withinHome makes sure no symlink along the path escapes the home.
func withinHome(home, rel string) (string, error) {
	physicalHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", err
	}
	full := filepath.Join(physicalHome, rel)
	current := physicalHome
	for _, part := range strings.Split(rel, "/") {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return full, nil
			}
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s is a symbolic link; Hire does not follow links inside a home", part)
		}
	}
	return full, nil
}

func browseHome(home, rel string) (fileView, error) {
	rel, err := cleanRelative(rel)
	if err != nil {
		return fileView{}, err
	}
	if !isBrowsable(rel) {
		return fileView{}, fmt.Errorf("%s is outside the browsable parts of the home", rel)
	}
	full, err := withinHome(home, rel)
	if err != nil {
		return fileView{}, err
	}
	info, err := os.Lstat(full)
	if err != nil {
		return fileView{}, err
	}
	view := fileView{Path: rel, Dir: info.IsDir(), Size: info.Size(), Modified: info.ModTime(), Writable: isWritable(rel) && !info.IsDir()}
	if info.IsDir() {
		entries, err := os.ReadDir(full)
		if err != nil {
			return fileView{}, err
		}
		for _, entry := range entries {
			child := entry.Name()
			childRel := child
			if rel != "" {
				childRel = rel + "/" + child
			}
			if !isBrowsable(childRel) {
				continue
			}
			childInfo, err := entry.Info()
			if err != nil {
				continue
			}
			if childInfo.Mode()&os.ModeSymlink != 0 {
				continue
			}
			view.Entries = append(view.Entries, fileEntry{Name: child, Path: childRel, Dir: entry.IsDir(), Size: childInfo.Size(), Modified: childInfo.ModTime()})
		}
		sort.Slice(view.Entries, func(i, j int) bool {
			if view.Entries[i].Dir != view.Entries[j].Dir {
				return view.Entries[i].Dir
			}
			return view.Entries[i].Name < view.Entries[j].Name
		})
		return view, nil
	}
	if !info.Mode().IsRegular() {
		return fileView{}, fmt.Errorf("%s is not a regular file", rel)
	}
	f, err := os.Open(full)
	if err != nil {
		return fileView{}, err
	}
	defer f.Close()
	buf := make([]byte, fileReadLimit+1)
	n, err := readFull(f, buf)
	if err != nil {
		return fileView{}, err
	}
	data := buf[:n]
	if n > fileReadLimit {
		data = data[:fileReadLimit]
		view.Truncated = true
	}
	if !utf8.Valid(data) {
		view.Binary = true
		view.Writable = false
		return view, nil
	}
	view.Content = string(data)
	return view, nil
}

func readFull(f *os.File, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := f.Read(buf[total:])
		total += n
		if err != nil {
			if err.Error() == "EOF" {
				return total, nil
			}
			return total, err
		}
		if n == 0 {
			break
		}
	}
	return total, nil
}

func writeHomeFile(home, rel, content string) error {
	rel, err := cleanRelative(rel)
	if err != nil {
		return err
	}
	if !isWritable(rel) {
		return fmt.Errorf("only files under work/ and state/ can be written here; definition files have their own editor")
	}
	if len(content) > fileWriteLimit {
		return fmt.Errorf("file is larger than %d bytes", fileWriteLimit)
	}
	full, err := withinHome(home, rel)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(full); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%s exists and is not a regular file", rel)
	}
	return writeFileAtomic(full, []byte(content), false)
}

func deleteHomeFile(home, rel string) error {
	rel, err := cleanRelative(rel)
	if err != nil {
		return err
	}
	if !isWritable(rel) {
		return fmt.Errorf("only files under work/ and state/ can be removed here")
	}
	full, err := withinHome(home, rel)
	if err != nil {
		return err
	}
	info, err := os.Lstat(full)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", rel)
	}
	return os.Remove(full)
}
