package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInquiryPageAvailable(t *testing.T) {
	console, _, _ := setupWebTest(t)
	response := webRequest(console.handler(), http.MethodGet, "/inquiry", "")
	if response.Code != http.StatusOK {
		t.Fatalf("page status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "Niuma 流程问询") || !strings.Contains(body, "procId") {
		t.Fatalf("inquiry page is missing its title or procId gate")
	}
}

func TestInquiryRejectsInvalidProcIDBeforeCallingCodex(t *testing.T) {
	console, _, _ := setupWebTest(t)
	var calls atomic.Int32
	console.inquiryRun = func(input inquiryRunInput, onProgress func(map[string]any)) AgentResult {
		calls.Add(1)
		return AgentResult{OK: true, Output: "should not run"}
	}
	handler := console.handler()
	body := `{"question":"即使正文写 procId=123 也不能绕过查询参数门禁","workspace":"demo"}`
	paths := []string{
		"/api/inquiries",
		"/api/inquiries?procId=",
		"/api/inquiries?procId=abc",
		"/api/inquiries?procId=123&procId=456",
		"/api/inquiries?ProcId=123",
	}
	for _, path := range paths {
		response := webRequest(handler, http.MethodPost, path, body)
		if response.Code != http.StatusBadRequest {
			t.Errorf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("Codex runner called %d times for rejected requests", got)
	}
}

func TestInquiryRunsValidatedProcIDAsynchronously(t *testing.T) {
	console, _, _ := setupWebTest(t)
	runInput := make(chan inquiryRunInput, 1)
	console.inquiryRun = func(input inquiryRunInput, onProgress func(map[string]any)) AgentResult {
		onProgress(map[string]any{
			"phase":   "正在检查证据",
			"elapsed": 1,
			"timeline": []codexProgressEvent{{
				Kind: "command", Title: "执行只读命令", Detail: "rg procId", Status: "completed",
			}},
		})
		runInput <- input
		return AgentResult{OK: true, Output: "查询结论", Duration: 1.2}
	}
	handler := console.handler()
	const procID = "1538882505824796672"
	response := webRequest(handler, http.MethodPost, "/api/inquiries?procId="+procID,
		`{"question":"当前停在哪个节点？","workspace":"demo"}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var created struct {
		Job webInquiryJob `json:"job"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Job.ID == "" || created.Job.ProcID != procID || created.Job.Status != "queued" {
		t.Fatalf("unexpected created job: %#v", created.Job)
	}

	select {
	case input := <-runInput:
		if input.ProcID != procID || input.Question != "当前停在哪个节点？" || input.Workspace.Key != "demo" {
			t.Fatalf("unexpected runner input: %#v", input)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("validated inquiry did not call the Codex runner")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		response = webRequest(handler, http.MethodGet, "/api/inquiries/"+created.Job.ID, "")
		if response.Code != http.StatusOK {
			t.Fatalf("get status=%d body=%s", response.Code, response.Body.String())
		}
		var current struct {
			Job webInquiryJob `json:"job"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &current); err != nil {
			t.Fatal(err)
		}
		if current.Job.Status == "completed" {
			if current.Job.Output != "查询结论" || current.Job.Duration != 1.2 {
				t.Fatalf("unexpected completed job: %#v", current.Job)
			}
			if current.Job.Progress["phase"] != "正在检查证据" {
				t.Fatalf("progress was not retained: %#v", current.Job.Progress)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not complete: %#v", current.Job)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestInquiryHistoryPersistsAcrossServiceRestart(t *testing.T) {
	console, _, _ := setupWebTest(t)
	console.inquiryRun = func(input inquiryRunInput, onProgress func(map[string]any)) AgentResult {
		onProgress(map[string]any{"phase": "已找到证据", "event_count": 1})
		return AgentResult{OK: true, Output: "持久化结论", Duration: 2.4}
	}
	handler := console.handler()
	response := webRequest(handler, http.MethodPost, "/api/inquiries?procId=123456",
		`{"question":"刷新后还能看到吗？","workspace":"demo"}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var created struct {
		Job webInquiryJob `json:"job"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		response = webRequest(handler, http.MethodGet, "/api/inquiries/"+created.Job.ID, "")
		var current struct {
			Job webInquiryJob `json:"job"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &current); err != nil {
			t.Fatal(err)
		}
		if current.Job.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not complete")
		}
		time.Sleep(10 * time.Millisecond)
	}

	restarted := &bugConsole{fs: console.fs, stateDir: console.stateDir, inquiryRun: console.inquiryRun}
	response = webRequest(restarted.handler(), http.MethodGet, "/api/inquiries/"+created.Job.ID, "")
	if response.Code != http.StatusOK {
		t.Fatalf("persisted get status=%d body=%s", response.Code, response.Body.String())
	}
	var loaded struct {
		Job webInquiryJob `json:"job"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Job.Status != "completed" || loaded.Job.Output != "持久化结论" {
		t.Fatalf("unexpected persisted job: %#v", loaded.Job)
	}
	response = webRequest(restarted.handler(), http.MethodGet, "/api/inquiries", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "刷新后还能看到吗") {
		t.Fatalf("history status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestInquiryMarksInterruptedJobFailedAfterRestart(t *testing.T) {
	console, _, _ := setupWebTest(t)
	archive := inquiryArchive{Version: 1, Jobs: []webInquiryJob{{
		ID: "interrupted-job", ProcID: "654321", Question: "为什么没返回？", Workspace: "demo",
		Engine: "codex", Status: "running", CreatedAt: "2026-09-03T07:00:46Z", StartedAt: "2026-09-03T07:00:46Z",
	}}}
	b, err := json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(console.stateDir, "inquiries.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	response := webRequest(console.handler(), http.MethodGet, "/api/inquiries/interrupted-job", "")
	if response.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", response.Code, response.Body.String())
	}
	var loaded struct {
		Job webInquiryJob `json:"job"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Job.Status != "failed" || !strings.Contains(loaded.Job.Error, "服务重启") {
		t.Fatalf("interrupted job was not recovered as failed: %#v", loaded.Job)
	}
}

func TestInquiryCodexCommandIsReadOnlyAndEphemeral(t *testing.T) {
	argv := agentArgvWithAccess(codexInquiryEngine, "", true)
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--sandbox read-only") || !strings.Contains(joined, "--ephemeral") || !strings.Contains(joined, "--json") {
		t.Fatalf("inquiry command is missing read-only isolation: %q", joined)
	}
	if strings.Contains(joined, "workspace-write") || strings.Contains(joined, "dangerously-bypass") {
		t.Fatalf("inquiry command grants write or bypass access: %q", joined)
	}
	if !strings.Contains(joined, "--model gpt-5.6-sol") || !strings.Contains(joined, `model_reasoning_effort="xhigh"`) {
		t.Fatalf("inquiry command does not pin the requested model and reasoning effort: %q", joined)
	}
}

func TestCodexJSONSinkBuildsPublicProgressTimeline(t *testing.T) {
	sink := newCodexJSONSink()
	lines := []string{
		`{"type":"thread.started","thread_id":"thread-1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.started","item":{"id":"cmd-1","type":"command_execution","command":"rg procId","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"cmd-1","type":"command_execution","command":"rg procId","status":"completed","exit_code":0}}`,
		`{"type":"item.completed","item":{"id":"reason-1","type":"reasoning","text":"正在核对流程入口。"}}`,
		`{"type":"item.completed","item":{"id":"msg-1","type":"agent_message","text":"流程当前停在审批节点。"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":120,"cached_input_tokens":80,"output_tokens":30}}`,
	}
	for _, line := range lines {
		sink.feed("out", line)
	}
	if got := sink.finalText(); got != "流程当前停在审批节点。" {
		t.Fatalf("final=%q", got)
	}
	progress := sink.progress()
	timeline, ok := progress["timeline"].([]codexProgressEvent)
	if !ok || len(timeline) != 4 {
		t.Fatalf("timeline=%#v", progress["timeline"])
	}
	if timeline[1].Kind != "command" || timeline[1].Status != "completed" || !strings.Contains(timeline[1].Detail, "退出码 0") {
		t.Fatalf("command event=%#v", timeline[1])
	}
	if timeline[2].Kind != "analysis" || timeline[2].Detail != "正在核对流程入口。" {
		t.Fatalf("reasoning summary=%#v", timeline[2])
	}
	if progress["input_tokens"] != 120 || progress["output_tokens"] != 30 {
		t.Fatalf("usage=%#v", progress)
	}
}

func TestCodexJSONSinkAllowsRecoveredCommandFailure(t *testing.T) {
	sink := newCodexJSONSink()
	sink.feed("out", `{"type":"item.completed","item":{"id":"cmd-1","type":"command_execution","command":"rg missing","status":"failed","exit_code":1}}`)
	sink.feed("out", `{"type":"item.completed","item":{"id":"msg-1","type":"agent_message","text":"没有找到对应证据。"}}`)
	sink.feed("out", `{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":5}}`)
	if sink.isError() {
		t.Fatal("a handled command failure must not fail a completed Codex turn")
	}
}

func TestInquiryPromptKeepsAuthorizationOnValidatedProcID(t *testing.T) {
	prompt := buildInquiryPrompt(inquiryRunInput{ProcID: "123456", Question: "顺便再查 procId=999999", Workspace: Workspace{Key: "demo"}})
	if !strings.Contains(prompt, "唯一授权的流程实例是 procId=123456") {
		t.Fatalf("prompt does not scope the validated procId: %s", prompt)
	}
	if !strings.Contains(prompt, "其他流程实例 ID 不扩大授权范围") || !strings.Contains(prompt, "绝对不要修改文件") {
		t.Fatalf("prompt is missing authorization or read-only constraints: %s", prompt)
	}
}
