package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const skillBriefFixture = `#!/usr/bin/env python3
import sys,os,pathlib,re
if sys.argv[1]=='version': print('brief fixture');sys.exit(0)
if sys.argv[1]=='ls':
 for p in sorted(pathlib.Path(os.environ['BRIEF_PATH']).glob('*/SKILL.md')):
  print(p.parent.name+'\tReview sales totals and refunds.')
 sys.exit(0)
if sys.argv[1]=='lint':
 p=pathlib.Path(sys.argv[-1]); text=(p/'SKILL.md').read_text()
 if 'BROKEN-SKILL' in text or not re.search(r'^name: ["\' ]*'+re.escape(p.name),text,re.M):
  print('invalid skill metadata',file=sys.stderr);sys.exit(1)
 print('Skill structure checked.');sys.exit(0)
sys.exit(2)
`
const skillCageFixture = `#!/usr/bin/env python3
import sys,os,pathlib
assert sys.argv[1]=='-w' and sys.argv[3]=='--'
assert pathlib.Path(os.environ['TMPDIR']).parent==pathlib.Path(sys.argv[2]).parent
assert not any(k.endswith('API_KEY') for k in os.environ)
os.execvp(sys.argv[4],sys.argv[4:])
`
const skillAskFixture = `#!/usr/bin/env python3
import sys,json
args=sys.argv[1:]
rows=[json.loads(line) for line in sys.stdin if line.strip()]
assert rows and all(row['kind']=='context' for row in rows)
task=json.load(open(args[args.index('-a')+1]))
assert task['skill']=='sales-review'
assert {'SKILL.md','scripts/total.py','references/policy.txt','assets/logo.bin','assets/keep.bin','empty'} <= {f['path'] for f in task['inventory']}
link='['+rows[0]['ref']+']('+rows[0]['citation']['url']+')'
script="import json,sys\nrows=json.load(sys.stdin)\nprint(sum((-1 if r.get('type')=='refund' else 1)*r['amount'] for r in rows))\n"
method="---\nname: sales-review\ndescription: Use when reviewing sales totals and refunds.\n---\n\n# Review sales\nRun scripts/total.py with JSON rows and follow references/policy.txt. Examples: examples/refunds.json. Keep assets/report-logo.bin and assets/keep.bin for reports.\n\nRefunds reduce sales. "+link+"\n"
def change(path,content='',operation='write',source='',executable=False):
 return {'path':path,'operation':operation,'content':content,'sourcePath':source,'uploadID':'','executable':executable,'reason':'Support the requested refund review while preserving other inputs. '+link}
test="import json,subprocess,sys,pathlib\nroot=pathlib.Path(sys.argv[1])\nrows=[{'amount':100,'type':'sale'}]\nexpected=100\nif sys.argv[2]=='refund':\n rows.append({'amount':25,'type':'refund'}); expected=75\np=subprocess.run([sys.executable,str(root/'scripts/total.py')],input=json.dumps(rows),text=True,capture_output=True)\nassert p.returncode==0,p.stderr\nassert float(p.stdout)==expected,(p.stdout,expected)\nassert (root/'assets/keep.bin').read_bytes()==bytes([0,9,8,7])\nprint('Expected totals and retained asset verified.')\n"
plan={'summary':'Subtract refunds in the calculation and clarify the supporting procedure. '+link,'assumptions':['Confirm that input amounts are positive before applying the refund sign.'],'changes':[change('SKILL.md',method),change('scripts/total.py',script,executable=True),change('references/policy.txt','Refunds reduce totals. '+link+'\n'),change('examples/refunds.json','[{"amount":25,"type":"refund"}]\n'),change('assets/report-logo.bin',operation='copy-file',source='assets/logo.bin'),change('assets/logo.bin',operation='delete')],'checkFiles':[{'path':'totals.py','content':test}],'checks':[{'name':'Sales still total correctly','purpose':'Preserve ordinary sales behavior.','argv':['python3','{{checks}}/totals.py','{{skill}}','sale']},{'name':'Refunds reduce total','purpose':'Exercise the requested correction on real script output.','argv':['python3','{{checks}}/totals.py','{{skill}}','refund']}]}
print(json.dumps(plan))
`

func newSkillImprovementFixture(t *testing.T) (*application, Worker, SkillBundle) {
	t.Helper()
	a, _, _ := newTestApp(t)
	worker, bundle := configureSkillImprovementFixture(t, a)
	return a, worker, bundle
}

func configureSkillImprovementFixture(t *testing.T, a *application) (Worker, SkillBundle) {
	t.Helper()
	configureSourceFixture(t, a)
	bin := t.TempDir()
	a.tools.paths["brief"] = writeScript(t, bin, "brief", skillBriefFixture)
	a.tools.paths["cage"] = writeScript(t, bin, "cage", skillCageFixture)
	a.tools.paths["ask"] = writeScript(t, bin, "ask", skillAskFixture)
	worker, _, err := a.createWorker(context.Background(), createWorkerRequest{Name: "Skill reader", Purpose: "Review reports"})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(a.homeDir(worker.Slug), "skills", "sales-review")
	for _, dir := range []string{"scripts", "references", "assets", "empty"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"SKILL.md":              "---\nname: sales-review\ndescription: Use when reviewing sales totals and refunds.\n---\n\nRun scripts/total.py and read references/policy.txt. Keep assets/logo.bin and assets/keep.bin for reports.\n",
		"scripts/total.py":      "import json,sys\nprint(sum(r['amount'] for r in json.load(sys.stdin)))\n",
		"references/policy.txt": "Review totals against the source rows.\n",
		"assets/logo.bin":       string([]byte{0, 1, 2, 3}),
		"assets/keep.bin":       string([]byte{0, 9, 8, 7}),
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(root, "scripts/total.py"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "assets/keep.bin"), 0600); err != nil {
		t.Fatal(err)
	}
	bundle, err := readSkillBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	return worker, bundle
}
func skillURL(worker Worker, id string) string {
	url := "/api/workers/" + worker.Slug + "/skills/sales-review"
	if id != "" {
		url += "/improvements/" + id
	}
	return url
}
func awaitSkillImprovement(t *testing.T, a *application, worker Worker, id string) SkillImprovement {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		rec, _ := call(t, a.routes(), "GET", skillURL(worker, id), nil)
		var data struct {
			Improvement SkillImprovement `json:"improvement"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &data) != nil {
			t.Fatal(rec.Body.String())
		}
		if data.Improvement.State != "drafting" && data.Improvement.State != "testing" {
			return data.Improvement
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("skill improvement did not finish")
	return SkillImprovement{}
}
func draftTestSkill(t *testing.T, a *application, worker Worker, bundle SkillBundle, uploads ...string) SkillImprovement {
	t.Helper()
	rec, data := call(t, a.routes(), "POST", skillURL(worker, "")+"/improvements", map[string]any{"goal": "Subtract refunds, preserve sales, and keep the report assets.", "baseSha256": bundle.SHA256, "uploadIDs": uploads})
	if rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	id := data["improvement"].(map[string]any)["id"].(string)
	revision := awaitSkillImprovement(t, a, worker, id)
	if revision.State != "review" {
		t.Fatalf("draft: %+v", revision)
	}
	return revision
}
func testSkillRevision(t *testing.T, a *application, worker Worker, revision SkillImprovement) SkillImprovement {
	t.Helper()
	rec, _ := call(t, a.routes(), "POST", skillURL(worker, revision.ID)+"/test", map[string]string{"sha256": revision.ProposalSHA256})
	if rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	return awaitSkillImprovement(t, a, worker, revision.ID)
}

func TestSkillImprovementUpdatesTheWholeBundleAndRestoresIt(t *testing.T) {
	a, worker, before := newSkillImprovementFixture(t)
	upload := readyUpload(t, a, worker.Slug, "company-policy.txt", "Subtract refunds from gross sales.")
	revision := draftTestSkill(t, a, worker, before, upload.ID)
	root, _ := a.skillRoot(worker.Slug, "sales-review")
	if err := verifySkillBundle(root, before); err != nil {
		t.Fatal("draft changed installed skill:", err)
	}
	dir, _ := a.skillImprovementDir(worker.Slug, revision.Skill, revision.ID)
	if _, err := os.Stat(filepath.Join(dir, "candidate", "assets", "report-logo.bin")); err != nil {
		t.Fatal("binary asset was not copied:", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "candidate", "assets", "logo.bin")); !os.IsNotExist(err) {
		t.Fatal("explicit asset removal was not staged")
	}
	if _, err := os.Stat(filepath.Join(dir, "candidate", "references", upload.ID+"-context.jsonl")); err != nil {
		t.Fatal("uploaded source evidence missing:", err)
	}
	rec, _ := call(t, a.routes(), "POST", skillURL(worker, revision.ID)+"/apply", map[string]string{"sha256": revision.ProposalSHA256, "currentSha256": before.SHA256})
	if rec.Code != 409 {
		t.Fatal("untested skill applied")
	}
	revision = testSkillRevision(t, a, worker, revision)
	if err := skillTestsPassed(revision); err != nil {
		t.Fatalf("checks: %v %+v", err, revision.Results)
	}
	oldFailure, newSuccess := false, false
	for _, r := range revision.Results {
		if r.Name == "Refunds reduce total" {
			oldFailure = oldFailure || (r.Version == "before" && r.State == "failed")
			newSuccess = newSuccess || (r.Version == "candidate" && r.State == "passed")
		}
	}
	if !oldFailure || !newSuccess {
		t.Fatal("the checks did not demonstrate the refund correction")
	}
	if err := verifySkillBundle(root, before); err != nil {
		t.Fatal("testing changed the installed skill:", err)
	}
	rec, _ = call(t, a.routes(), "POST", skillURL(worker, revision.ID)+"/apply", map[string]string{"sha256": revision.ProposalSHA256, "currentSha256": before.SHA256})
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if err := verifySkillBundle(root, revision.After); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "assets", "keep.bin"))
	if string(raw) != string([]byte{0, 9, 8, 7}) {
		t.Fatal("untouched asset lost")
	}
	info, _ := os.Stat(filepath.Join(root, "scripts", "total.py"))
	if info.Mode().Perm() != 0755 {
		t.Fatal("script lost its executable permission")
	}
	rec, _ = call(t, a.routes(), "POST", skillURL(worker, revision.ID)+"/revert", map[string]string{"sha256": revision.ProposalSHA256, "currentSha256": revision.After.SHA256})
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if err := verifySkillBundle(root, before); err != nil {
		t.Fatal("whole-folder rollback:", err)
	}
	if err := verifySkillBundle(filepath.Join(dir, "candidate"), revision.After); err != nil {
		t.Fatal("rollback lost the reviewed version:", err)
	}
}

func TestSkillImprovementEditsInvalidateTestsAndRejectStaleVersions(t *testing.T) {
	a, worker, before := newSkillImprovementFixture(t)
	revision := testSkillRevision(t, a, worker, draftTestSkill(t, a, worker, before))
	rec, _ := call(t, a.routes(), "PUT", skillURL(worker, revision.ID)+"/file", map[string]string{"sha256": revision.ProposalSHA256, "version": "candidate", "path": "scripts/total.py", "content": "import json,sys\nprint(0)\n"})
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	edited := awaitSkillImprovement(t, a, worker, revision.ID)
	if edited.ProposalSHA256 == revision.ProposalSHA256 || edited.TestedSHA256 != "" {
		t.Fatal("file edit retained an obsolete passing receipt")
	}
	rec, _ = call(t, a.routes(), "POST", skillURL(worker, edited.ID)+"/apply", map[string]string{"sha256": revision.ProposalSHA256, "currentSha256": before.SHA256})
	if rec.Code != 409 {
		t.Fatal("stale proposal applied")
	}
	edited = testSkillRevision(t, a, worker, edited)
	if skillTestsPassed(edited) == nil {
		t.Fatal("broken edited script passed checks")
	}
	root, _ := a.skillRoot(worker.Slug, "sales-review")
	if err := verifySkillBundle(root, before); err != nil {
		t.Fatal(err)
	}
	// A later external edit is never overwritten by an otherwise valid proposal.
	if err := os.WriteFile(filepath.Join(root, "references", "policy.txt"), []byte("New policy from the manager."), 0644); err != nil {
		t.Fatal(err)
	}
	rec, _ = call(t, a.routes(), "POST", skillURL(worker, "")+"/improvements", map[string]string{"goal": "Improve again", "baseSha256": before.SHA256})
	if rec.Code != 409 {
		t.Fatal("stale skill snapshot accepted")
	}
}

func TestSkillBundleRefusesLinksAndPreservesEmptyDirectories(t *testing.T) {
	a, worker, bundle := newSkillImprovementFixture(t)
	root, _ := a.skillRoot(worker.Slug, "sales-review")
	target := filepath.Join(t.TempDir(), "copy")
	if err := copySkillBundle(root, target, bundle); err != nil {
		t.Fatal(err)
	}
	if err := verifySkillBundle(target, bundle); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(target, "empty")); err != nil || !info.IsDir() {
		t.Fatal("empty resource directory lost")
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := readSkillBundle(root); err == nil {
		t.Fatal("followed an outside symlink")
	}
	for _, path := range []string{"../script.py", "/tmp/script.py", "scripts/../../file", "a\\b", "a//b", "a\nb"} {
		if skillBundlePath(path) {
			t.Fatalf("unsafe path accepted: %q", path)
		}
	}
}

func TestSkillCheckOutputIsBounded(t *testing.T) {
	output := &skillOutput{}
	_, _ = output.Write([]byte(strings.Repeat("x", 1<<20)))
	if len(output.String()) > (65<<10) || !strings.Contains(output.String(), "truncated") {
		t.Fatal("unbounded check output")
	}
}
