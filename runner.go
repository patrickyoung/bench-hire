package main

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// Runner owns the loops that Hire adds around Tend: work loops that perform
// one durable transition at a time, and a scheduler that turns due routines
// into requests. Neither loop calls a model; the runs do.
type Runner struct {
	app      *application
	paused   atomic.Bool
	workers  int
	mu       sync.Mutex
	lastWork time.Time
	lastTick time.Time
	active   atomic.Int32
	errors   []string
}

type runnerStatus struct {
	Paused   bool      `json:"paused"`
	Workers  int       `json:"workers"`
	Active   int       `json:"active"`
	LastWork time.Time `json:"lastWork"`
	LastTick time.Time `json:"lastTick"`
	Errors   []string  `json:"errors,omitempty"`
}

func newRunner(app *application, workers int) *Runner {
	if workers < 1 {
		workers = 1
	}
	return &Runner{app: app, workers: workers}
}

func (r *Runner) Status() runnerStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return runnerStatus{Paused: r.paused.Load(), Workers: r.workers, Active: int(r.active.Load()), LastWork: r.lastWork, LastTick: r.lastTick, Errors: append([]string(nil), r.errors...)}
}

func (r *Runner) SetPaused(paused bool) { r.paused.Store(paused) }

func (r *Runner) note(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	line := r.app.now().Format("15:04:05") + " " + err.Error()
	r.errors = append(r.errors, line)
	if len(r.errors) > 20 {
		r.errors = r.errors[len(r.errors)-20:]
	}
	log.Print("hire runner: ", err)
}

func (r *Runner) Start(ctx context.Context) {
	for i := 0; i < r.workers; i++ {
		go r.workLoop(ctx)
	}
	go r.scheduleLoop(ctx)
}

func (r *Runner) workLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if r.paused.Load() {
			sleepContext(ctx, 2*time.Second)
			continue
		}
		r.active.Add(1)
		code, err := r.app.jobs.Work(ctx)
		r.active.Add(-1)
		r.mu.Lock()
		r.lastWork = r.app.now()
		r.mu.Unlock()
		switch {
		case err != nil:
			r.note(err)
			sleepContext(ctx, 5*time.Second)
		case code == 0:
			continue
		default:
			sleepContext(ctx, 2*time.Second)
		}
	}
}

func (r *Runner) scheduleLoop(ctx context.Context) {
	r.Tick(ctx, r.app.now())
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if r.paused.Load() {
				continue
			}
			r.Tick(ctx, r.app.now())
		}
	}
}

// Tick queues one request for every routine that is due. A routine that was
// missed for a long time gets one catch-up run, not one per missed slot, and
// then advances past now.
func (r *Runner) Tick(ctx context.Context, now time.Time) int {
	r.mu.Lock()
	r.lastTick = now
	r.mu.Unlock()
	workers, err := r.app.store.Workers()
	if err != nil {
		r.note(err)
		return 0
	}
	queued := 0
	for _, w := range workers {
		if err := r.app.store.reconcileIntakes(w.Slug); err != nil {
			r.note(err)
			continue
		}
		if w.RetiringAt != nil && w.RetiredAt == nil {
			if _, err := r.app.retireWorker(ctx, w.Slug, false); err != nil {
				r.note(err)
			}
			continue
		}
		if !w.Enabled || w.CheckState != "valid" {
			continue
		}
		requests, err := r.app.store.Requests(w.Slug)
		if err != nil {
			r.note(err)
			continue
		}
		for _, request := range requests {
			if request.Runs {
				if err := r.app.submitRequest(ctx, request); err != nil {
					r.note(err)
				}
			}
		}
		routines, err := r.app.store.Routines(w.Slug)
		if err != nil {
			r.note(err)
			continue
		}
		for _, routine := range routines {
			added, err := r.app.queueScheduledRoutine(ctx, w.Slug, routine.ID, now)
			if err != nil {
				r.note(err)
			}
			if added {
				queued++
			}
		}
	}
	return queued
}

// Admission and schedule advancement share the same lock as editing,
// deleting and retiring. A stale scheduler snapshot cannot restore a routine.
func (a *application) queueScheduledRoutine(ctx context.Context, slug, id string, now time.Time) (bool, error) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	worker, err := a.store.Worker(slug)
	if err != nil {
		return false, err
	}
	if !worker.Enabled || worker.CheckState != "valid" || worker.RetiringAt != nil || worker.RetiredAt != nil {
		return false, nil
	}
	routine, err := a.store.Routine(slug, id)
	if errors.Is(err, errNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !routine.Enabled || routine.NextDue.After(now) {
		return false, nil
	}
	due, next := routine.NextDue, routine.NextDue
	for !next.After(now) {
		due = next
		next = nextDue(routine.Every, routine.At, routine.Weekday, next, a.location)
	}
	req, err := a.queueRoutineLocked(ctx, worker, routine, due, now)
	if err != nil {
		return false, err
	}
	routine.NextDue, routine.LastQueued, routine.LastRequest = next, &now, req.ID
	return true, a.store.SaveRoutine(routine)
}

func sleepContext(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
