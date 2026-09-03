package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store keeps Hire's own records as JSON files a person can cat. Workers and
// requests are raw and never rewritten after creation; routines carry the one
// derived field (next due) that advances as the scheduler queues work.
type Store struct {
	root string
	mu   sync.Mutex
}

var errNotFound = errors.New("not found")

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,119}$`)

type Worker struct {
	Slug         string      `json:"slug"`
	Name         string      `json:"name"`
	Purpose      string      `json:"purpose"`
	Model        string      `json:"model"`
	Network      bool        `json:"network"`
	Enabled      bool        `json:"enabled"`
	CreatedAt    time.Time   `json:"createdAt"`
	DeployedAt   *time.Time  `json:"deployedAt,omitempty"`
	CheckState   string      `json:"checkState"`
	CheckMessage string      `json:"checkMessage,omitempty"`
	Receipt      homeReceipt `json:"receipt"`
}

type Routine struct {
	ID           string     `json:"id"`
	WorkerSlug   string     `json:"workerSlug"`
	Title        string     `json:"title"`
	Instructions string     `json:"instructions"`
	Check        string     `json:"check,omitempty"`
	Every        string     `json:"every"`
	At           string     `json:"at,omitempty"`
	Weekday      int        `json:"weekday"`
	Enabled      bool       `json:"enabled"`
	NextDue      time.Time  `json:"nextDue"`
	LastQueued   *time.Time `json:"lastQueued,omitempty"`
	LastRequest  string     `json:"lastRequest,omitempty"`
	Source       string     `json:"source"`
	CreatedAt    time.Time  `json:"createdAt"`
}

type Request struct {
	ID         string    `json:"id"`
	WorkerSlug string    `json:"workerSlug"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title"`
	Text       string    `json:"text"`
	Check      string    `json:"check,omitempty"`
	Source     string    `json:"source"`
	ParentID   string    `json:"parentId,omitempty"`
	NotBefore  time.Time `json:"notBefore"`
	Model      string    `json:"model"`
	Network    bool      `json:"network"`
	Runs       bool      `json:"runs"`
	CreatedAt  time.Time `json:"createdAt"`
}

type Plan struct {
	RequestID string          `json:"requestId"`
	Model     string          `json:"model"`
	Fallback  bool            `json:"fallback"`
	Error     string          `json:"error,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"`
	Actions   []PlannedAction `json:"actions"`
	Session   string          `json:"session,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

type PlannedAction struct {
	Title        string `json:"title"`
	Instructions string `json:"instructions"`
	When         string `json:"when"`
	Repeat       string `json:"repeat"`
	RequestID    string `json:"requestId,omitempty"`
	RoutineID    string `json:"routineId,omitempty"`
}

type Settings struct {
	Model string `json:"model"`
}

type ModelProof struct {
	Model   string    `json:"model"`
	OK      bool      `json:"ok"`
	Output  string    `json:"output"`
	Command string    `json:"command"`
	At      time.Time `json:"at"`
}

func newStore(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create Hire records root: %w", err)
	}
	return &Store{root: root}, nil
}

func validSlug(slug string) bool { return slugPattern.MatchString(slug) }
func validID(id string) bool     { return idPattern.MatchString(id) }

func (s *Store) workerDir(slug string) string { return filepath.Join(s.root, slug) }

func (s *Store) RequestPath(slug, id string) string {
	return filepath.Join(s.workerDir(slug), "requests", id+".json")
}

func (s *Store) Workers() ([]Worker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	var workers []Worker
	for _, entry := range entries {
		if !entry.IsDir() || !validSlug(entry.Name()) {
			continue
		}
		var w Worker
		if err := readJSON(filepath.Join(s.root, entry.Name(), "worker.json"), &w); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		workers = append(workers, w)
	}
	sort.Slice(workers, func(i, j int) bool { return workers[i].CreatedAt.After(workers[j].CreatedAt) })
	return workers, nil
}

func (s *Store) Worker(slug string) (Worker, error) {
	if !validSlug(slug) {
		return Worker{}, errNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var w Worker
	if err := readJSON(filepath.Join(s.workerDir(slug), "worker.json"), &w); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Worker{}, errNotFound
		}
		return Worker{}, err
	}
	return w, nil
}

func (s *Store) SaveWorker(w Worker) error {
	if !validSlug(w.Slug) {
		return fmt.Errorf("invalid worker slug %q", w.Slug)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.workerDir(w.Slug)
	for _, sub := range []string{"requests", "routines", "plans"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return err
		}
	}
	return writeJSONAtomic(filepath.Join(dir, "worker.json"), w, false)
}

func (s *Store) DeleteWorker(slug string) error {
	if !validSlug(slug) {
		return errNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.RemoveAll(s.workerDir(slug))
}

func (s *Store) Routines(slug string) ([]Routine, error) {
	if !validSlug(slug) {
		return nil, errNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var routines []Routine
	err := readAllJSON(filepath.Join(s.workerDir(slug), "routines"), func(path string) error {
		var r Routine
		if err := readJSON(path, &r); err != nil {
			return err
		}
		routines = append(routines, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(routines, func(i, j int) bool { return routines[i].CreatedAt.Before(routines[j].CreatedAt) })
	return routines, nil
}

func (s *Store) Routine(slug, id string) (Routine, error) {
	if !validSlug(slug) || !validID(id) {
		return Routine{}, errNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var r Routine
	if err := readJSON(filepath.Join(s.workerDir(slug), "routines", id+".json"), &r); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Routine{}, errNotFound
		}
		return Routine{}, err
	}
	return r, nil
}

func (s *Store) SaveRoutine(r Routine) error {
	if !validSlug(r.WorkerSlug) || !validID(r.ID) {
		return fmt.Errorf("invalid routine identity %q/%q", r.WorkerSlug, r.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONAtomic(filepath.Join(s.workerDir(r.WorkerSlug), "routines", r.ID+".json"), r, false)
}

func (s *Store) DeleteRoutine(slug, id string) error {
	if !validSlug(slug) || !validID(id) {
		return errNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(filepath.Join(s.workerDir(slug), "routines", id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return errNotFound
	}
	return err
}

func (s *Store) Requests(slug string) ([]Request, error) {
	if !validSlug(slug) {
		return nil, errNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var requests []Request
	err := readAllJSON(filepath.Join(s.workerDir(slug), "requests"), func(path string) error {
		var r Request
		if err := readJSON(path, &r); err != nil {
			return err
		}
		requests = append(requests, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].CreatedAt.After(requests[j].CreatedAt) })
	return requests, nil
}

func (s *Store) Request(slug, id string) (Request, error) {
	if !validSlug(slug) || !validID(id) {
		return Request{}, errNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var r Request
	if err := readJSON(s.RequestPath(slug, id), &r); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Request{}, errNotFound
		}
		return Request{}, err
	}
	return r, nil
}

// CreateRequest refuses to overwrite: a request is what arrived, and Tend
// would refuse a changed definition under the same id anyway.
func (s *Store) CreateRequest(r Request) error {
	if !validSlug(r.WorkerSlug) || !validID(r.ID) {
		return fmt.Errorf("invalid request identity %q/%q", r.WorkerSlug, r.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONAtomic(s.RequestPath(r.WorkerSlug, r.ID), r, true)
}

func (s *Store) SavePlan(slug string, p Plan) error {
	if !validSlug(slug) || !validID(p.RequestID) {
		return fmt.Errorf("invalid plan identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONAtomic(filepath.Join(s.workerDir(slug), "plans", p.RequestID+".json"), p, false)
}

func (s *Store) Plan(slug, id string) (Plan, error) {
	if !validSlug(slug) || !validID(id) {
		return Plan{}, errNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var p Plan
	if err := readJSON(filepath.Join(s.workerDir(slug), "plans", id+".json"), &p); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Plan{}, errNotFound
		}
		return Plan{}, err
	}
	return p, nil
}

func (s *Store) Settings() (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v Settings
	err := readJSON(filepath.Join(s.root, "settings.json"), &v)
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, nil
	}
	return v, err
}

func (s *Store) SaveSettings(v Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONAtomic(filepath.Join(s.root, "settings.json"), v, false)
}

func (s *Store) ModelProof() (ModelProof, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v ModelProof
	err := readJSON(filepath.Join(s.root, "model-proof.json"), &v)
	if errors.Is(err, os.ErrNotExist) {
		return ModelProof{}, false, nil
	}
	return v, err == nil, err
}

func (s *Store) SaveModelProof(v ModelProof) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONAtomic(filepath.Join(s.root, "model-proof.json"), v, false)
}

func readAllJSON(dir string, each func(path string) error) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if err := each(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// writeJSONAtomic writes through a same-directory temporary file. With
// exclusive set it refuses to replace an existing record.
func writeJSONAtomic(path string, v any, exclusive bool) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data, exclusive)
}

func writeFileAtomic(path string, data []byte, exclusive bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if exclusive {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("%s already exists", filepath.Base(path))
		}
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if exclusive {
		if err := os.Link(tmpName, path); err != nil {
			_ = os.Remove(tmpName)
			return fmt.Errorf("%s already exists", filepath.Base(path))
		}
		return os.Remove(tmpName)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
