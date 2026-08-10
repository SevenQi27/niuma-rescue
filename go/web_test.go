package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeBugStore struct {
	records []Record
}

func (f *fakeBugStore) listRecords() ([]Record, error) {
	return f.records, nil
}

func (f *fakeBugStore) createRecord(fields map[string]any) (*Record, error) {
	rec := Record{RecordID: "rec-web-1", Fields: fields}
	f.records = append(f.records, rec)
	return &rec, nil
}

func (f *fakeBugStore) updateRecord(recordID string, fields map[string]any) error {
	for i := range f.records {
		if f.records[i].RecordID != recordID {
			continue
		}
		for key, value := range fields {
			f.records[i].Fields[key] = value
		}
		return nil
	}
	return errf("record not found")
}

func setupWebTest(t *testing.T) (*bugConsole, *fakeBugStore, *int) {
	t.Helper()
	dir := t.TempDir()
	workspaces := filepath.Join(dir, "workspaces.json")
	content := `{"default":"demo","items":{"demo":{"path":"` + dir + `","scm":"git","base":"main","target_branch":"main"}}}`
	if err := os.WriteFile(workspaces, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	oldCfg, oldCache := cfg, wsCache
	cfg = &Config{EngineBugFix: "claude", EngineBugReview: "codex", WorkspacesFile: workspaces, StateDir: dir}
	wsMu.Lock()
	wsCache = nil
	wsMu.Unlock()
	t.Cleanup(func() {
		cfg = oldCfg
		wsMu.Lock()
		wsCache = oldCache
		wsMu.Unlock()
	})
	fake := &fakeBugStore{}
	fired := 0
	return &bugConsole{fs: fake, stateDir: dir, fire: func() { fired++ }}, fake, &fired
}

func webRequest(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestWebConsoleAvailableOnLAN(t *testing.T) {
	console, _, _ := setupWebTest(t)
	handler := console.handler()
	if got := webRequest(handler, http.MethodGet, "/api/bugs", "").Code; got != http.StatusOK {
		t.Fatalf("api status=%d", got)
	}
	if got := webRequest(handler, http.MethodGet, "/", "").Code; got != http.StatusOK {
		t.Fatalf("page status=%d", got)
	}
}

func TestWebConsoleCreateEditAndStartBug(t *testing.T) {
	console, fake, fired := setupWebTest(t)
	handler := console.handler()
	create := `{"title":"checkout fails","description":"steps and expected result","workspace":"demo","fix_agent":"codex","reporter":"Alice","start":false}`
	response := webRequest(handler, http.MethodPost, "/api/bugs", create)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	if len(fake.records) != 1 || fieldText(fake.records[0].Fields[FStatus]) != SSetup {
		t.Fatalf("unexpected records: %#v", fake.records)
	}
	if *fired != 0 {
		t.Fatalf("draft must not trigger dispatcher: %d", *fired)
	}

	update := `{"title":"checkout returns 500","description":"updated steps","clarification":"request id 42","workspace":"demo","fix_agent":"cursor"}`
	response = webRequest(handler, http.MethodPut, "/api/bugs/rec-web-1", update)
	if response.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", response.Code, response.Body.String())
	}
	if fieldText(fake.records[0].Fields[FAgentCode]) != "cursor" || fieldText(fake.records[0].Fields[FAgentReview]) != "codex" {
		t.Fatalf("unexpected agents: %#v", fake.records[0].Fields)
	}

	response = webRequest(handler, http.MethodPost, "/api/bugs/rec-web-1/start", `{}`)
	if response.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", response.Code, response.Body.String())
	}
	if fieldText(fake.records[0].Fields[FStatus]) != SBug || *fired != 1 {
		t.Fatalf("start did not trigger: status=%s fired=%d", fieldText(fake.records[0].Fields[FStatus]), *fired)
	}

	response = webRequest(handler, http.MethodPut, "/api/bugs/rec-web-1", update)
	if response.Code != http.StatusConflict {
		t.Fatalf("active bug must be read-only: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWebBugJSONShape(t *testing.T) {
	console, _, _ := setupWebTest(t)
	response := webRequest(console.handler(), http.MethodGet, "/api/meta", "")
	if response.Code != http.StatusOK {
		t.Fatalf("meta status=%d", response.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body["workspaces"].([]any)) != 1 {
		t.Fatalf("unexpected meta: %#v", body)
	}
}

func TestWebConsoleUploadsAndServesBugAttachment(t *testing.T) {
	console, fake, _ := setupWebTest(t)
	handler := console.handler()
	create := `{"title":"layout broken","description":"button overlaps text","workspace":"demo","fix_agent":"codex","start":false}`
	response := webRequest(handler, http.MethodPost, "/api/bugs", create)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("images", "screenshot.png")
	if err != nil {
		t.Fatal(err)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	if _, err := part.Write(png); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/bugs/rec-web-1/images", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", response.Code, response.Body.String())
	}
	var uploaded struct {
		Bug webBug `json:"bug"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}
	if len(uploaded.Bug.Images) != 1 || uploaded.Bug.Images[0].Name != "screenshot.png" || uploaded.Bug.Images[0].MIME != "image/png" {
		t.Fatalf("unexpected images: %#v", uploaded.Bug.Images)
	}
	imageResponse := webRequest(handler, http.MethodGet, uploaded.Bug.Images[0].URL, "")
	if imageResponse.Code != http.StatusOK || imageResponse.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("image response status=%d type=%s", imageResponse.Code, imageResponse.Header().Get("Content-Type"))
	}
	if !bytes.Equal(imageResponse.Body.Bytes(), png) {
		t.Fatal("served image differs from upload")
	}

	worktree := t.TempDir()
	(&App{}).writeBugDossier(worktree, fake.records[0].RecordID, &fake.records[0])
	stored := storedWebImages(fieldText(fake.records[0].Fields[FLog]))
	if len(stored) != 1 {
		t.Fatalf("stored metadata: %#v", stored)
	}
	if _, err := os.Stat(filepath.Join(worktree, dossierDir, "BUG-rec-web-1", "attachments", stored[0].File)); err != nil {
		t.Fatalf("dossier attachment missing: %v", err)
	}
}

func TestWebConsoleAcceptsMoreThanFiveAttachments(t *testing.T) {
	console, fake, _ := setupWebTest(t)
	handler := console.handler()
	create := `{"title":"many files","description":"multiple logs and screenshots","workspace":"demo","fix_agent":"codex","start":false}`
	if response := webRequest(handler, http.MethodPost, "/api/bugs", create); response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for i := 0; i < 7; i++ {
		part, err := writer.CreateFormFile("images", "screenshot-"+itoa(i)+".png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(append([]byte("\x89PNG\r\n\x1a\n"), byte(i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/bugs/rec-web-1/images", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", response.Code, response.Body.String())
	}
	if got := len(storedWebImages(fieldText(fake.records[0].Fields[FLog]))); got != 7 {
		t.Fatalf("attachments=%d want=7", got)
	}
}

func TestWebAttachmentTypeSupportsDocuments(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		body     []byte
		ext      string
		mimeType string
	}{
		{name: "pdf", fileName: "report.pdf", body: []byte("%PDF-1.7\n"), ext: ".pdf", mimeType: "application/pdf"},
		{name: "xls", fileName: "report.xls", body: []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}, ext: ".xls", mimeType: "application/vnd.ms-excel"},
		{name: "csv", fileName: "report.csv", body: []byte("name,value\nA,1\n"), ext: ".csv", mimeType: "text/csv"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ext, mimeType := webAttachmentType(test.fileName, test.body)
			if ext != test.ext || mimeType != test.mimeType {
				t.Fatalf("type=(%q,%q), want=(%q,%q)", ext, mimeType, test.ext, test.mimeType)
			}
		})
	}

	var body bytes.Buffer
	writer := zip.NewWriter(&body)
	for _, name := range []string{"[Content_Types].xml", "xl/workbook.xml"} {
		part, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("<xml/>")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	ext, mimeType := webAttachmentType("report.xlsx", body.Bytes())
	if ext != ".xlsx" || mimeType != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Fatalf("xlsx type=(%q,%q)", ext, mimeType)
	}
}

func TestWebConsoleReturnsLiveBugPipeline(t *testing.T) {
	console, fake, _ := setupWebTest(t)
	fake.records = append(fake.records, Record{RecordID: "rec-live", Fields: map[string]any{
		FTitle: "checkout fails", FDesc: "steps", FTaskType: TaskBug, FStatus: SBug,
		FWorkspace: "demo", FAgentCode: "codex", FAgentReview: "cursor",
	}})
	appendBugProgress(console.stateDir, "rec-live", "prepare", "done", "Niuma", "branch ready", 0)
	appendBugProgress(console.stateDir, "rec-live", "investigate", "running", "codex", "reading code", 0)

	response := webRequest(console.handler(), http.MethodGet, "/api/bugs/rec-live/pipeline", "")
	if response.Code != http.StatusOK {
		t.Fatalf("pipeline status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Pipeline webBugPipeline `json:"pipeline"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Pipeline.Live || len(body.Pipeline.Events) != 2 {
		t.Fatalf("unexpected pipeline: %#v", body.Pipeline)
	}
	if body.Pipeline.Steps[0].State != "done" || body.Pipeline.Steps[1].State != "running" {
		t.Fatalf("unexpected steps: %#v", body.Pipeline.Steps)
	}
}

func TestWebConsoleBackfillsPipelineFromAgentArtifacts(t *testing.T) {
	console, fake, _ := setupWebTest(t)
	fake.records = append(fake.records, Record{RecordID: "rec-old", Fields: map[string]any{
		FTitle: "old bug", FDesc: "steps", FTaskType: TaskBug, FStatus: SMerge,
		FWorkspace: "demo", FAgentCode: "codex", FAgentReview: "cursor",
	}})
	dir := filepath.Join(console.stateDir, "agent-runs", "20260807-150000-codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"cwd":"/tmp/BUG-rec-old","engine":"codex","return":0,"duration":12,"time":"2026-08-07 15:00:00 +0800"}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("你是 Bug 调查 Agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "result.txt"), []byte("DIAGNOSED\nroot cause"), 0o644); err != nil {
		t.Fatal(err)
	}

	response := webRequest(console.handler(), http.MethodGet, "/api/bugs/rec-old/pipeline", "")
	var body struct {
		Pipeline webBugPipeline `json:"pipeline"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Pipeline.Events) != 1 || body.Pipeline.Events[0].Stage != "investigate" {
		t.Fatalf("artifact was not backfilled: %#v", body.Pipeline.Events)
	}
	if stepState(body.Pipeline.Steps, "delivery") != "waiting" {
		t.Fatalf("merge step must wait for a human: %#v", body.Pipeline.Steps)
	}
}
