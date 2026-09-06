package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This fixture exercises Hire's public-command and browser boundaries. It
// deliberately does not simulate Hone's provenance proof; suite_test.go uses
// the real Hone, Ask, Trail and Brief for that contract.
func configureLearningFixture(t *testing.T, a *application) {
	t.Helper()
	cases := `
  history)
    printf '%s\n' '{"kind":"session","id":"recovery","path":"/fixture/recovery.jsonl","summary":"Report corrected against source records","started":"2026-09-03T09:00:00Z"}' '{"kind":"session","id":"first-pass","path":"/fixture/first-pass.jsonl","summary":"Passed on the first attempt","started":"2026-09-03T10:00:00Z"}'
    exit 0;;
  learn)
    operation=; proposal=; home=; session=
    while [ "$#" -gt 0 ]; do
      case "$1" in
        -into|-n|-m) shift;;
        -why) operation=inspect;;
        -prepare|-show|-admit) operation=$1; shift; proposal=$1;;
        *) if [ -z "$home" ]; then home=$1; else session=$1; fi;;
      esac
      shift
    done
    case "$operation" in
      inspect)
        [ "$session" != first-pass.jsonl ] || exit 1
        printf 'STUMBLE: the report omitted a refund.\nRECOVERY: recomputed signed source amounts; check passed.\n';;
      -prepare)
        [ "$session" != first-pass.jsonl ] || { echo 'hone: nothing worth keeping' >&2; exit 1; }
        mkdir -p "$home/.agent/learning/proposals"
        printf '%s\n' '---' 'name: learned-method' 'description: Check reports against signed source records.' '---' '' 'Include refunds before comparing totals to the source records.' > "$home/.agent/learning/proposals/$proposal"
        cat "$home/.agent/learning/proposals/$proposal";;
      -show) cat "$home/.agent/learning/proposals/$proposal";;
      -admit)
        mkdir -p "$home/skills/learned-method"
        cp "$home/.agent/learning/proposals/$proposal" "$home/skills/learned-method/SKILL.md"
        printf 'Added the reviewed lesson.\n';;
      *) exit 2;;
    esac
    exit 0;;
`
	agent := strings.Replace(fakeAgent, "case \"$cmd\" in", "case \"$cmd\" in"+cases, 1)
	if err := os.WriteFile(a.tools.path("agent"), []byte(agent), 0o755); err != nil {
		t.Fatal(err)
	}
	a.tools.paths["hone"] = a.tools.path("agent")
	a.tools.paths["brief"] = writeScript(t, t.TempDir(), "brief", `#!/bin/sh
if [ "$1" = version ]; then echo 'brief fixture'; exit 0; fi
for skill in "$BRIEF_PATH"/*/SKILL.md; do
  [ -f "$skill" ] || continue
  dir=${skill%/SKILL.md}; name=${dir##*/}
  printf '%s\tA supplied procedure.\n' "$name"
done
`)
}

func TestLearningReviewAndArchive(t *testing.T) {
	a, _, _ := newTestApp(t)
	configureLearningFixture(t, a)
	h := a.routes()
	worker, _, err := a.createWorker(context.Background(), createWorkerRequest{Name: "Learner", Purpose: "Check reports."})
	if err != nil {
		t.Fatal(err)
	}
	slug := worker.Slug
	url := "/api/workers/" + slug
	noLesson, _ := call(t, h, http.MethodPost, url+"/learning", map[string]any{"operation": "prepare", "skill": "learned-method", "session": "first-pass.jsonl"})
	if noLesson.Code != http.StatusConflict || !strings.Contains(noLesson.Body.String(), "No useful lesson was found") {
		t.Fatalf("no-lesson explanation: %d %s", noLesson.Code, noLesson.Body.String())
	}
	in := map[string]any{"operation": "prepare", "skill": "learned-method", "session": "recovery.jsonl"}
	rec, prepared := call(t, h, http.MethodPost, url+"/learning", in)
	if rec.Code != http.StatusOK {
		t.Fatalf("prepare: %d %s", rec.Code, rec.Body.String())
	}
	proposal := prepared["proposal"].(string)
	path := filepath.Join(a.homeDir(slug), ".agent", "learning", "proposals", proposal)
	if err := os.WriteFile(path, []byte("Changed after review.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, _ = call(t, h, http.MethodPost, url+"/learning", map[string]any{"operation": "admit", "proposal": proposal, "sha256": prepared["sha256"]})
	if rec.Code != http.StatusConflict {
		t.Fatalf("changed proposal admitted: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(a.homeDir(slug), "skills", "learned-method")); !os.IsNotExist(err) {
		t.Fatalf("preparation or stale admission changed skills: %v", err)
	}
	rec, _ = call(t, h, http.MethodDelete, url, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("retire: %d %s", rec.Code, rec.Body.String())
	}
	for _, operation := range []string{"inspect", "show", "prepare", "admit"} {
		in := map[string]any{"operation": operation, "skill": "learned-method", "session": "recovery.jsonl", "proposal": proposal, "sha256": contentSHA256([]byte("Changed after review.\n"))}
		rec, _ := call(t, h, http.MethodPost, url+"/learning", in)
		want := http.StatusOK
		if operation == "prepare" || operation == "admit" {
			want = http.StatusConflict
		}
		if rec.Code != want {
			t.Fatalf("archive %s: %d %s", operation, rec.Code, rec.Body.String())
		}
	}
}
