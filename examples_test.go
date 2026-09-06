package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const repairedExampleSlug = `#!/bin/sh
LC_ALL=C
export LC_ALL
if [ "$#" -lt 1 ]; then echo 'Supply text to normalize' >&2; exit 2; fi
slug=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9][^a-z0-9]*/-/g; s/^-//; s/-$//')
if [ -z "$slug" ]; then echo 'Text contains no ASCII letters or digits' >&2; exit 2; fi
printf '%s\n' "$slug"
`

func TestExamplesInstallBeforeAdmissionAndCheckRealMistakes(t *testing.T) {
	for _, id := range []string{"reporting", "documents", "maintenance"} {
		t.Run(id, func(t *testing.T) {
			a, _, _ := newTestApp(t)
			log := filepath.Join(t.TempDir(), "calls.log")
			t.Setenv("FAKE_ASK_LOG", log)
			h := a.routes()
			rec, payload := call(t, h, http.MethodPost, "/api/workers", map[string]string{"exampleId": id})
			if rec.Code != http.StatusCreated {
				t.Fatal(rec.Body.String())
			}
			slug := payload["worker"].(map[string]any)["slug"].(string)
			worker, err := a.store.Worker(slug)
			if err != nil || worker.StarterTask == nil || worker.ExampleID != id || !worker.Receipt.Valid {
				t.Fatalf("incomplete example published: %+v %v", worker, err)
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatal("hiring an example called a model")
			}
			example, err := loadExample(id)
			if err != nil {
				t.Fatal(err)
			}
			home := a.homeDir(slug)
			for _, rel := range example.Files {
				if _, err := os.Stat(filepath.Join(home, rel)); err != nil {
					t.Fatalf("example published before %s was installed: %v", rel, err)
				}
			}
			for _, rel := range []string{"GOAL.md", "AGENTS.md"} {
				data, err := os.ReadFile(filepath.Join(home, rel))
				if err != nil || strings.Contains(string(data), "2026-09-03") {
					t.Fatal("particular reporting dates leaked into the standing job")
				}
			}
			intake, err := a.intake(context.Background(), worker, *worker.StarterTask)
			if err != nil {
				t.Fatal(err)
			}
			req := intake.Request
			if req.Check != "sh ../tools/example-check" || req.Text != strings.TrimSpace(example.Task.Text) {
				t.Fatal("example task lost its supplied instructions or check")
			}
			if err := writeFileAtomic(filepath.Join(home, "REQUEST.md"), []byte(renderRequestFile(req)), false); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(home, "work", "requests", req.ID)
			if err := writeFileAtomic(filepath.Join(out, "RESULT.md"), []byte("Fixture report: judge the explanation separately.\n"), false); err != nil {
				t.Fatal(err)
			}
			check := func(want int) {
				t.Helper()
				_, stderr, code, err := runCommand(context.Background(), "/bin/sh", []string{filepath.Join(home, "tools", "example-check")}, filepath.Join(home, "work"), nil, os.Environ(), 5*time.Second)
				if err != nil || code != want {
					t.Fatalf("check=%d, want %d: %s %v", code, want, stderr, err)
				}
			}
			check(1) // missing data, or the shipped buggy program
			var file, good, bad string
			switch id {
			case "reporting":
				file = filepath.Join(out, "totals.tsv")
				good = "region\tnet_cents\nEast\t5000\nNorth\t10000\nSouth\t17000\nTOTAL\t32000\n"
				bad = "region\tnet_cents\nEast\t5000\nNorth\t12000\nSouth\t17000\nTOTAL\t34000\n"
			case "documents":
				file = filepath.Join(out, "actions.tsv")
				good = "request_id\towner\tdue\taction\tsources\nM-101\tPriya\t2026-09-12\tRenew pump service\tM-101,M-103\nM-102\tDylan\tunknown\tRequest valve quote\tM-102\n"
				bad = strings.Replace(good, "2026-09-12", "2026-09-10", 1)
			case "maintenance":
				file = filepath.Join(home, "work", "project", "slug.sh")
				good = repairedExampleSlug
				bad = strings.Replace(good, "exit 2", "exit 0", 1)
			}
			if err := writeFileAtomic(file, []byte(bad), false); err != nil {
				t.Fatal(err)
			}
			check(1)
			if err := writeFileAtomic(file, []byte(good), false); err != nil {
				t.Fatal(err)
			}
			check(0)
			if id == "documents" {
				if err := writeFileAtomic(file, []byte(strings.Replace(good, "unknown", "2026-09-15", 1)), false); err != nil {
					t.Fatal(err)
				}
				check(1) // inventing the missing deadline must fail
			}
			read, err := browseHome(home, "tools/example-check")
			if err != nil || read.Writable || read.Content == "" {
				t.Fatal("supplied checks must be inspectable and outside the worker's write roots")
			}
		})
	}
}

func TestConcurrentExampleHiresCannotDeleteEachOthersHome(t *testing.T) {
	a, _, _ := newTestApp(t)
	results := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Go(func() {
			_, _, err := a.createWorker(context.Background(), createWorkerRequest{ExampleID: "maintenance"})
			results <- err
		})
	}
	group.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent hires=%d", successes)
	}
	worker, err := a.store.Worker("maintenance-worker")
	if err != nil || !worker.Receipt.Valid {
		t.Fatal("successful hire lost its record")
	}
	if _, err := os.Stat(filepath.Join(a.homeDir(worker.Slug), "tools", "example-check")); err != nil {
		t.Fatal("failed duplicate hire removed the successful home")
	}
}
