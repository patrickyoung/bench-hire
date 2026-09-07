package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Offline public-command fixtures. Optional contract tests below use the real
// selected Context and Cite; these fixtures never discover host programs.
const sourceContextFixture = `#!/usr/bin/env python3
import sys,json,hashlib
rows=[]
try:
 for line in sys.stdin:
  row=json.loads(line)
  assert row['kind']=='context' and row['version']==1
  assert row['content'] is not None and row['citation']['locator']
  ref='ctx:'+row['source']+':'+hashlib.sha256((row['source']+'\0'+row['id']).encode()).hexdigest()[:32]
  assert row.get('ref',ref)==ref
  row['ref']=ref
  rows.append(row)
except Exception as e:
 print('invalid source record: '+str(e),file=sys.stderr);sys.exit(2)
if not rows: sys.exit(1)
if sys.argv[1]=='merge':
 for row in rows: print(json.dumps(row,separators=(',',':')))
`
const sourceCiteFixture = `#!/usr/bin/env python3
import sys,json,re
rows=[json.loads(line) for line in open(sys.argv[1])]
links={r['ref']:'['+r['ref']+']('+r['citation']['url']+')' for r in rows}
text=sys.stdin.read()
refs=re.findall(r'ctx:[a-zA-Z0-9:._-]+',text)
if not refs or any(links.get(ref,'NEVER') not in text for ref in refs):
 print('citations do not match the supplied evidence',file=sys.stderr);sys.exit(1)
print(text,end='')
`

func configureSourceFixture(t *testing.T, a *application) {
	t.Helper()
	dir := t.TempDir()
	a.tools.paths["context"] = writeScript(t, dir, "context", sourceContextFixture)
	a.tools.paths["cite"] = writeScript(t, dir, "cite", sourceCiteFixture)
	if err := a.store.SaveModelProof(ModelProof{Model: a.model, OK: true, Output: "ok", At: a.now()}); err != nil {
		t.Fatal(err)
	}
}
func uploadFixture(t *testing.T, a *application, slug, name string, raw []byte) (int, Upload) {
	t.Helper()
	var body bytes.Buffer
	multi := multipart.NewWriter(&body)
	part, err := multi.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(raw)
	_ = multi.Close()
	req := httptest.NewRequest("POST", "http://127.0.0.1:8790/api/uploads?worker="+slug, &body)
	req.Header.Set("Content-Type", multi.FormDataContentType())
	req.Header.Set("X-Hire-Token", a.token)
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, req)
	var result struct {
		Upload Upload `json:"upload"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &result)
	if rec.Code != 201 {
		t.Log(rec.Body.String())
	}
	return rec.Code, result.Upload
}
func readyUpload(t *testing.T, a *application, slug, name, text string) Upload {
	t.Helper()
	code, u := uploadFixture(t, a, slug, name, []byte(text))
	if code != 201 || u.State != "ready" {
		t.Fatalf("upload %d: %+v", code, u)
	}
	return u
}
func TestUploadsCarryContextIntoTasksRoutinesAndSkills(t *testing.T) {
	a, jobs, _ := newTestApp(t)
	configureSourceFixture(t, a)
	worker, _, err := a.createWorker(context.Background(), createWorkerRequest{Name: "Source reader", Purpose: "Summarize evidence"})
	if err != nil {
		t.Fatal(err)
	}
	u := readyUpload(t, a, worker.Slug, "handbook.md", "# Policy\nReport net sales; retain qualifications.\n")
	if !strings.HasPrefix(u.Ref, "ctx:uploads:") || u.EvidenceSHA256 == "" {
		t.Fatalf("missing Context provenance: %+v", u)
	}
	response, err := a.intake(context.Background(), worker, intakeRequest{Text: "Summarize this policy", UploadIDs: []string{u.ID}})
	if err != nil {
		t.Fatal(err)
	}
	request := response.Actions[0]
	if len(request.Uploads) != 1 || !strings.Contains(renderRequestFile(request), u.Ref) {
		t.Fatalf("request lost source: %+v", request)
	}
	if code, err := jobs.Work(context.Background()); err != nil || code != 0 {
		t.Fatalf("run %d: %v", code, err)
	}
	home := a.homeDir(worker.Slug)
	if err := verifyUploadRefs(home, request.Uploads); err != nil {
		t.Fatal(err)
	}
	candidate := []byte("Report net sales. [" + u.Ref + "](" + u.URL + ")\n")
	if err := os.MkdirAll(a.askDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := a.checkSourceCitations(context.Background(), home, request.Uploads, candidate); err != nil {
		t.Fatal(err)
	}
	if err := a.checkSourceCitations(context.Background(), home, request.Uploads, []byte("[ctx:uploads:invented](/sources/missing)")); err == nil {
		t.Fatal("invented source accepted")
	}
	rec, payload := call(t, a.routes(), "POST", "/api/workers/"+worker.Slug+"/routines", map[string]any{"instructions": "Use the handbook", "every": "daily", "at": "09:00", "uploadIDs": []string{u.ID}})
	if rec.Code != 201 {
		t.Fatalf("routine: %s", rec.Body.String())
	}
	rid := payload["id"].(string)
	routine, err := a.store.Routine(worker.Slug, rid)
	if err != nil {
		t.Fatal(err)
	}
	occurrence, err := a.queueRoutineLocked(context.Background(), worker, routine, a.now().Add(time.Hour), a.now())
	if err != nil || len(occurrence.Uploads) != 1 {
		t.Fatalf("routine source lost: %+v %v", occurrence, err)
	}
	rec, _ = call(t, a.routes(), "POST", "/api/workers/"+worker.Slug+"/skills", map[string]any{"name": "review-policy", "description": "Use for policy reviews", "method": string(candidate), "uploadIDs": []string{u.ID}})
	if rec.Code != 201 {
		t.Fatalf("skill: %s", rec.Body.String())
	}
	installed, err := os.ReadFile(filepath.Join(home, "skills/review-policy/SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(installed, []byte("not verified by a successful run")) || !bytes.Contains(installed, []byte(u.Ref)) {
		t.Fatal("skill lost teaching provenance")
	}
	if _, err := os.Stat(filepath.Join(home, "skills/review-policy/references", u.ID, "context.jsonl")); err != nil {
		t.Fatal(err)
	}
	rec, again := call(t, a.routes(), "POST", "/api/workers/"+worker.Slug+"/requests/"+request.ID+"/rerun", map[string]any{})
	if rec.Code != 201 {
		t.Fatalf("repeat task: %s", rec.Body.String())
	}
	fresh, err := a.store.Request(worker.Slug, again["request"].(map[string]any)["id"].(string))
	if err != nil || len(fresh.Uploads) != 1 || fresh.Uploads[0].EvidenceSHA256 != u.EvidenceSHA256 {
		t.Fatalf("repeated task lost its source evidence: %+v %v", fresh, err)
	}
	// An input edit is not silently repaired or treated as the reviewed version.
	if err := os.WriteFile(filepath.Join(home, request.Uploads[0].TextPath), []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := verifyUploadRefs(home, request.Uploads); err == nil {
		t.Fatal("altered imported reference accepted")
	}
}
func TestUploadScopeAndLimits(t *testing.T) {
	a, _, _ := newTestApp(t)
	configureSourceFixture(t, a)
	worker, _, err := a.createWorker(context.Background(), createWorkerRequest{Name: "Owner", Purpose: "Read"})
	if err != nil {
		t.Fatal(err)
	}
	u := readyUpload(t, a, worker.Slug, "..\\manual.md", "Source text")
	if u.Name != "manual.md" {
		t.Fatalf("unsafe name %q", u.Name)
	}
	if _, _, _, err := a.checkedUpload(u.ID, "someone-else"); err == nil {
		t.Fatal("cross-worker attachment accepted")
	}
	if _, err := a.importUploads(a.homeDir(worker.Slug), worker.Slug, "inputs/uploads", []string{u.ID, u.ID}); err == nil {
		t.Fatal("duplicate attachment accepted")
	}
	for _, tc := range []struct {
		name   string
		raw    []byte
		status int
	}{{"empty.txt", nil, 400}, {"raw.bin", []byte{0, 1, 2}, 415}, {"huge.txt", bytes.Repeat([]byte("x"), uploadFileLimit+1), 413}} {
		code, _ := uploadFixture(t, a, "", tc.name, tc.raw)
		if code != tc.status {
			t.Errorf("%s status %d", tc.name, code)
		}
	}
	dir, _ := a.uploadDir(u.ID)
	_ = os.WriteFile(filepath.Join(dir, "context.jsonl"), []byte("{}\n"), 0600)
	if _, err := a.uploadEvidence(context.Background(), worker.Slug, []string{u.ID}); err == nil {
		t.Fatal("modified source snapshot accepted")
	}
	global := readyUpload(t, a, "", "common.txt", "Reusable company source")
	if _, _, _, err := a.checkedUpload(global.ID, worker.Slug); err != nil {
		t.Fatal(err)
	}
}
func TestMediaReadingAndSourceSkillDraft(t *testing.T) {
	a, _, _ := newTestApp(t)
	configureSourceFixture(t, a)
	worker, _, err := a.createWorker(context.Background(), createWorkerRequest{Name: "Learner", Purpose: "Review reports"})
	if err != nil {
		t.Fatal(err)
	}
	var picture bytes.Buffer
	_ = png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	t.Setenv("FAKE_ASK_REPLY", writeTestReply(t, t.TempDir(), "reading.txt", "A diagram with two connected boxes. Details uncertain.\n"))
	code, u := uploadFixture(t, a, worker.Slug, "diagram.png", picture.Bytes())
	if code != 201 || u.State != "processing" {
		t.Fatalf("image not processed: %d %+v", code, u)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		u, err = a.uploadStatus(u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if u.State != "processing" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if u.State != "ready" || u.Method != "ask" || u.Ref == "" {
		t.Fatalf("media reading: %+v", u)
	}
	method := "1. Review the diagram. [" + u.Ref + "](" + u.URL + ")\n2. Ask about unclear details."
	reply, _ := json.Marshal(map[string]string{"name": "read-diagram", "description": "Use for diagram reviews", "method": method})
	t.Setenv("FAKE_ASK_REPLY", writeTestReply(t, t.TempDir(), "skill.json", string(reply)))
	rec, _ := call(t, a.routes(), "POST", "/api/workers/"+worker.Slug+"/skills/drafts", map[string]any{"goal": "Learn to review diagrams", "uploadIDs": []string{u.ID}})
	if rec.Code != 202 {
		t.Fatalf("draft: %s", rec.Body.String())
	}
	var result map[string]any
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, payload := call(t, a.routes(), "GET", "/api/workers/"+worker.Slug+"/skills/drafts", nil)
		items := payload["drafts"].([]any)
		if len(items) > 0 {
			result = items[0].(map[string]any)
			if result["draft"].(map[string]any)["state"] != "drafting" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if result == nil || result["draft"].(map[string]any)["state"] != "ready" {
		t.Fatalf("draft failed: %+v", result)
	}
	draft := result["draft"].(map[string]any)
	rec, _ = call(t, a.routes(), "POST", "/api/workers/"+worker.Slug+"/skills", map[string]any{"name": "read-diagram", "description": "Review diagrams", "method": method, "uploadIDs": []string{u.ID}, "draftID": draft["id"], "draftSha256": result["sha256"]})
	if rec.Code != 201 {
		t.Fatalf("admit draft: %s", rec.Body.String())
	}
}
func officeZip(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for name, value := range parts {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(value))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func TestOfficeSourcesAreUsableAndBounded(t *testing.T) {
	docs := []struct {
		name  string
		parts map[string]string
		want  []string
	}{
		{"manual.docx", map[string]string{"word/document.xml": `<document><body><p><r><t>Weekly review</t></r></p><p><r><t>Check refunds.</t></r></p></body></document>`}, []string{"Weekly review", "Check refunds."}},
		{"sales.xlsx", map[string]string{"xl/workbook.xml": `<workbook><sheets><sheet name="Revenue" id="rId1"/></sheets></workbook>`, "xl/worksheets/sheet1.xml": `<worksheet><sheetData><row><c r="A1" t="inlineStr"><is><t>Net sales</t></is></c><c r="C1"><v>42</v></c></row></sheetData></worksheet>`}, []string{"Sheet: Revenue", "A1\tNet sales", "C1\t42"}},
		{"brief.pptx", map[string]string{"ppt/presentation.xml": `<presentation/>`, "ppt/slides/slide1.xml": `<slide><p><r><t>Risks</t></r></p></slide>`}, []string{"slide1.xml", "Risks"}},
	}
	for _, tc := range docs {
		t.Run(tc.name, func(t *testing.T) {
			raw, _, method, err := extractUpload(tc.name, officeZip(t, tc.parts))
			if err != nil {
				t.Fatal(err)
			}
			if method == "ask" {
				t.Fatal("text Office content unnecessarily sent to a model")
			}
			for _, want := range tc.want {
				if !strings.Contains(string(raw), want) {
					t.Errorf("missing %q: %s", want, raw)
				}
			}
		})
	}
	if _, _, _, err := extractUpload("zip.zip", officeZip(t, map[string]string{"../oops": "data"})); err == nil {
		t.Fatal("arbitrary archive accepted")
	}
}
func TestRealSuiteUploadContext(t *testing.T) {
	bin := os.Getenv("HIRE_INTEGRATION_BIN_DIR")
	if bin == "" {
		t.Skip("set HIRE_INTEGRATION_BIN_DIR to validate real Context/Cite contracts")
	}
	bin, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := newTestApp(t)
	a.tools.paths["context"] = filepath.Join(bin, "context")
	a.tools.paths["cite"] = filepath.Join(bin, "cite")
	u := readyUpload(t, a, "", "evidence.md", "A retained source with exact citations.")
	refs, err := a.importUploads(a.dataRoot, "", "contract-inputs", []string{u.ID})
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(a.askDir, 0700)
	if err := a.checkSourceCitations(context.Background(), a.dataRoot, refs, []byte("A source. ["+u.Ref+"]("+u.URL+")")); err != nil {
		t.Fatal(err)
	}
	if err := a.checkSourceCitations(context.Background(), a.dataRoot, refs, []byte("An uncited claim.")); err == nil {
		t.Fatal("Cite accepted an uncited candidate")
	}
}

const sourceAskFixture = `#!/usr/bin/env python3
import sys,json
args=sys.argv[1:]
if args==['version']: print('ask fixture');sys.exit(0)
if '-schema' not in args:
 print('Two connected boxes. Check visual details against the original.');sys.exit(0)
rows=[json.loads(line) for line in sys.stdin if line.strip()]
assert rows and all(row['kind']=='context' and row['ref'] for row in rows)
links=' '.join('['+r['ref']+']('+r['citation']['url']+')' for r in rows)
schema=args[args.index('-schema')+1]
if schema.endswith('source-skill-schema.json'):
 print(json.dumps({'name':'review-sources','description':'Use when reviewing source reports.','method':'1. Read the supplied source and keep its qualifications. '+links+'\n2. Check totals and record missing information.'}))
elif schema.endswith('source-draft-schema.json'):
 task=json.load(open(args[args.index('-a')+1]))
 assert set(task['managerDraft'])=={'name','description','content'}
 result={'name':'source-notes','description':'','content':'','memories':[],'assumptions':[],'questions':['Who owns updates to these materials?']}
 if task['purpose']=='memory':
  assert 'existingMemory' in task
  result['memories']=[{'topic':'Refunds in weekly reviews','content':'Include refunds and identify missing records. '+links,'basis':'stated','reason':'Keep reviews consistent with the handbook.','reviewAfter':'When the handbook changes.'},{'topic':'Review ownership','content':'The reporting team may own this review. '+links,'basis':'inferred','reason':'The handbook describes reporting work but does not name an owner.','reviewAfter':'Confirm with the manager before relying on it.'}]
 else:
  result['content']='Review the supplied material, include refunds and identify missing records. '+links+'\n\nDeliver a concise report explaining discrepancies.'
  if task['purpose']=='skill': result.update(name='review-materials',description='Use when reviewing reports against supplied materials.')
 print(json.dumps(result))
elif schema.endswith('agent-builder-schema.json'):
 files={'goal':'# Job\nWrite useful reports from supplied records. '+links,'agents':'# Method\nRead REQUEST.md and produce a clear result with citations. '+links,'soul':'','plan':'','memory':'','heartbeat':''}
 print(json.dumps({'message':'Ready to review, using the supplied materials.','ready':True,'changes':[],'definition':{'name':'Source Writer','purpose':'Write reports from supplied materials.','network':False,'files':files,'checks':[]}}))
else:
 print('unexpected schema',file=sys.stderr);sys.exit(2)
`

func TestBuilderUsesContextSnapshotAndRetainsReferences(t *testing.T) {
	a, _, _ := newTestApp(t)
	configureSourceFixture(t, a)
	a.tools.paths["ask"] = writeScript(t, t.TempDir(), "ask", sourceAskFixture)
	u := readyUpload(t, a, "", "job-handbook.md", "Reports must include refunds and missing information.")
	job, err := a.startBuilderTurn("", "Hire a report writer", "single", u.ID)
	if err != nil {
		t.Fatal(err)
	}
	session, err := a.completeBuilderTurn(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(session.Proposal.Files.Goal, u.Ref) || !strings.Contains(session.Proposal.Files.Agents, "inputs/uploads/") {
		t.Fatal("proposal did not use retained sources")
	}
	worker, err := a.applyBuilderSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.homeDir(worker.Slug), "inputs/uploads", u.ID, "context.jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	// A later turn receives the same cited source even without selecting again.
	second, err := a.startBuilderTurn(worker.Slug, "Include examples", "single", u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.completeBuilderTurn(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	third, err := a.startBuilderTurn(worker.Slug, "Keep it brief", "single")
	if err != nil {
		t.Fatal(err)
	}
	if len(third.session.UploadIDs) != 1 {
		t.Fatal("conversation dropped earlier sources")
	}
	if _, err := a.completeBuilderTurn(context.Background(), third); err != nil {
		t.Fatal(err)
	}
}

func TestSourcesFollowPlannedWorkFeedbackAndRevisions(t *testing.T) {
	a, jobs, _ := newTestApp(t)
	configureSourceFixture(t, a)
	worker, _, err := a.createWorker(context.Background(), createWorkerRequest{Name: "Planner", Purpose: "Review source records"})
	if err != nil {
		t.Fatal(err)
	}
	policy := readyUpload(t, a, worker.Slug, "policy.md", "Include refunds.")
	correction := readyUpload(t, a, worker.Slug, "correction.csv", "Region,Refunds\nNorth,20\n")
	t.Setenv("FAKE_ASK_REPLY", writeTestReply(t, t.TempDir(), "plan.json", `{"actions":[{"title":"Review today","instructions":"Review refunds now","when":"now","repeat":""},{"title":"Review daily","instructions":"Review refunds each day","when":"2026-09-04T09:00:00Z","repeat":"daily"}]}`))
	result, err := a.intake(context.Background(), worker, intakeRequest{Text: "Review today and each morning", Plan: true, UploadIDs: []string{policy.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 1 || len(result.Routines) != 1 || len(result.Actions[0].Uploads) != 1 || len(result.Routines[0].Uploads) != 1 {
		t.Fatalf("plan lost references: %+v", result)
	}
	if code, err := jobs.Work(context.Background()); err != nil || code != 0 {
		t.Fatalf("run: %d %v", code, err)
	}
	task := result.Actions[0]
	_, view := call(t, a.routes(), "GET", "/api/workers/"+worker.Slug+"/requests/"+task.ID, nil)
	request := view["request"].(map[string]any)
	job := view["job"].(map[string]any)
	body := map[string]any{"decision": "changes-requested", "note": "Use these corrected refunds", "resultSha256": request["resultSha256"], "jobUpdatedUs": job["updated_us"], "uploadIDs": []string{correction.ID}}
	rec, payload := call(t, a.routes(), "POST", "/api/workers/"+worker.Slug+"/requests/"+task.ID+"/review", body)
	if rec.Code != 201 {
		t.Fatalf("feedback: %s", rec.Body.String())
	}
	revision, err := a.createRevision(context.Background(), worker.Slug, task.ID, payload["review"].(map[string]any)["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if len(revision.Uploads) != 2 || !strings.Contains(renderRequestFile(revision), correction.Ref) {
		t.Fatalf("revision lost sources: %+v", revision)
	}
	// Same note on different source bytes is a distinct reviewed instruction.
	body["uploadIDs"] = []string{policy.ID}
	rec, _ = call(t, a.routes(), "POST", "/api/workers/"+worker.Slug+"/requests/"+task.ID+"/review", body)
	if rec.Code != 201 {
		t.Fatalf("changed feedback source reused old receipt: %s", rec.Body.String())
	}
}
