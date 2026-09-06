package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// intakeBatch is the immutable record of one accepted request and its whole
// plan. It is committed before any job starts. The installed marker records
// only that the ordinary request/plan/routine files were materialized; Tend
// continues to own all execution state.
type intakeBatch struct {
	Schema   string         `json:"schema"`
	Response intakeResponse `json:"response"`
}

func (s *Store) commitIntake(response intakeResponse) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	slug, id := response.Request.WorkerSlug, response.Request.ID
	if !validSlug(slug) || !validID(id) {
		return false, errors.New("invalid intake identity")
	}
	batch := intakeBatch{Schema: "bench-hire-intake/v1", Response: response}
	if err := writeJSONAtomic(filepath.Join(s.workerDir(slug), "inbox", id+".json"), batch, true); err != nil {
		return false, err
	}
	return true, s.installIntake(batch)
}

// installIntake is called with Store.mu held. Repeating it after a process
// interruption may finish creating files but never overwrites a request or
// repeats a job. Once installed, it also never recreates a removed routine.
func (s *Store) installIntake(batch intakeBatch) error {
	r := batch.Response
	slug, id := r.Request.WorkerSlug, r.Request.ID
	if batch.Schema != "bench-hire-intake/v1" || !validSlug(slug) || !validID(id) || r.Plan == nil || r.Plan.RequestID != id {
		return errors.New("invalid saved intake")
	}
	marker := filepath.Join(s.workerDir(slug), "inbox", id+".installed")
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	requests := append([]Request{r.Request}, r.Actions...)
	for _, request := range requests {
		if request.WorkerSlug != slug || !validID(request.ID) || (request.ID != id && request.ParentID != id) {
			return errors.New("saved plan contains an unrelated task")
		}
		if err := ensureJSONRecord(s.RequestPath(slug, request.ID), request); err != nil {
			return err
		}
	}
	if err := ensureJSONRecord(filepath.Join(s.workerDir(slug), "plans", id+".json"), r.Plan); err != nil {
		return err
	}
	for _, routine := range r.Routines {
		if routine.WorkerSlug != slug || !validID(routine.ID) {
			return errors.New("saved plan contains an unrelated routine")
		}
		if err := ensureJSONRecord(filepath.Join(s.workerDir(slug), "routines", routine.ID+".json"), routine); err != nil {
			return err
		}
	}
	return writeFileAtomic(marker, []byte("installed\n"), true)
}

func ensureJSONRecord(path string, value any) error {
	want, err := json.Marshal(value)
	if err != nil {
		return err
	}
	old, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return writeJSONAtomic(path, value, true)
	}
	if err != nil {
		return err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, old); err != nil {
		return err
	}
	if !bytes.Equal(compact.Bytes(), want) {
		return fmt.Errorf("saved record %s differs from its accepted request", filepath.Base(path))
	}
	return nil
}

func (s *Store) reconcileIntakes(slug string) error {
	if !validSlug(slug) {
		return errNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return readAllJSON(filepath.Join(s.workerDir(slug), "inbox"), func(path string) error {
		marker := path[:len(path)-len(".json")] + ".installed"
		if _, err := os.Stat(marker); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		var batch intakeBatch
		if err := readJSON(path, &batch); err != nil {
			return err
		}
		if batch.Response.Request.WorkerSlug != slug {
			return errors.New("saved intake names another worker")
		}
		return s.installIntake(batch)
	})
}
