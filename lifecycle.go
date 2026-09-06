package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// Call with lifecycleMu held so a successful read remains valid until the
// mutation is saved. The route guard alone cannot protect concurrent changes.
func (a *application) mutableWorker(slug string) (Worker, error) {
	w, err := a.store.Worker(slug)
	if err != nil {
		return w, err
	}
	if w.RetiredAt != nil || w.RetiringAt != nil {
		return w, errors.New("this worker is retiring or retired; its work and history are available to read")
	}
	return w, nil
}

func (a *application) loadMutableWorker(w http.ResponseWriter, r *http.Request) (Worker, bool) {
	worker, err := a.mutableWorker(r.PathValue("slug"))
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, errNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, "worker", err.Error(), "")
		return Worker{}, false
	}
	return worker, true
}

func (a *application) retireWorker(ctx context.Context, slug string, begin bool) (Worker, error) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	w, err := a.store.Worker(slug)
	if err != nil {
		return Worker{}, err
	}
	if w.RetiredAt != nil {
		return w, nil
	}
	if err := a.store.reconcileIntakes(slug); err != nil {
		return w, err
	}
	if w.RetiringAt == nil {
		if !begin {
			return w, nil
		}
		now := a.now()
		w.RetiringAt = &now
		w.Enabled = false
		if err := a.store.SaveWorker(w); err != nil {
			return w, err
		}
	}
	routines, err := a.store.Routines(slug)
	if err != nil {
		return w, err
	}
	for _, routine := range routines {
		if routine.Enabled {
			routine.Enabled = false
			if err := a.store.SaveRoutine(routine); err != nil {
				return w, err
			}
		}
	}
	jobs, err := a.jobs.List(ctx)
	if err != nil {
		return w, err
	}
	var cancellationErrors []error
	for _, job := range jobs {
		if job.Cwd != a.homeDir(slug) {
			continue
		}
		if job.Status == "ready" || job.Status == "waiting" {
			if err := a.jobs.Cancel(ctx, job.ID); err != nil {
				cancellationErrors = append(cancellationErrors, err)
			}
		}
	}
	if len(cancellationErrors) > 0 {
		return w, fmt.Errorf("could not cancel all pending work: %w", errors.Join(cancellationErrors...))
	}
	// Re-read after cancellations: a worker may have claimed a job during the
	// first list. Never move its home or label it retired while it is active.
	jobs, err = a.jobs.List(ctx)
	if err != nil {
		return w, err
	}
	for _, job := range jobs {
		if job.Cwd != a.homeDir(slug) {
			continue
		}
		switch job.Status {
		case "ready", "running", "waiting", "unknown":
			return w, nil
		}
	}
	lock, err := lockExecution(a.store.workerDir(slug), false)
	if err != nil {
		// A skill admission or a run already in progress may finish. Do not
		// label its home archived while the controller is still changing it.
		return w, nil
	}
	defer lock.Close()
	now := a.now()
	w.RetiredAt = &now
	return w, a.store.SaveWorker(w)
}
