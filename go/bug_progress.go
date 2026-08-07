package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type bugProgressEvent struct {
	Stage     string  `json:"stage"`
	State     string  `json:"state"`
	Agent     string  `json:"agent,omitempty"`
	Detail    string  `json:"detail,omitempty"`
	Iteration int     `json:"iteration,omitempty"`
	TS        float64 `json:"ts"`
	Time      string  `json:"time"`
}

type webPipelineStep struct {
	Stage  string `json:"stage"`
	Label  string `json:"label"`
	State  string `json:"state"`
	Actor  string `json:"actor"`
	Detail string `json:"detail,omitempty"`
}

type webBugPipeline struct {
	BugID      string             `json:"bug_id"`
	Status     string             `json:"status"`
	Live       bool               `json:"live"`
	UpdatedAt  string             `json:"updated_at,omitempty"`
	ServerTime string             `json:"server_time"`
	Steps      []webPipelineStep  `json:"steps"`
	Events     []bugProgressEvent `json:"events"`
}

var bugProgressMu sync.Mutex

func bugProgressPath(root, recordID string) string {
	return filepath.Join(root, "bug-progress", recordID+".jsonl")
}

func resetBugProgress(root, recordID string) {
	bugProgressMu.Lock()
	defer bugProgressMu.Unlock()
	dir := filepath.Join(root, "bug-progress")
	if os.MkdirAll(dir, 0o755) == nil {
		_ = os.WriteFile(bugProgressPath(root, recordID), nil, 0o644)
	}
}

func appendBugProgress(root, recordID, stage, state, agent, detail string, iteration int) {
	if root == "" || !safeWebRecordID(recordID) {
		return
	}
	now := time.Now()
	event := bugProgressEvent{
		Stage: stage, State: state, Agent: agent, Detail: trunc(strings.TrimSpace(detail), 12000),
		Iteration: iteration, TS: float64(now.UnixNano()) / 1e9, Time: now.Format(time.RFC3339),
	}
	body, err := json.Marshal(event)
	if err != nil {
		return
	}
	bugProgressMu.Lock()
	defer bugProgressMu.Unlock()
	dir := filepath.Join(root, "bug-progress")
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	if file, err := os.OpenFile(bugProgressPath(root, recordID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		_, _ = file.Write(append(body, '\n'))
		_ = file.Close()
	}
}

func readBugProgress(root, recordID string) []bugProgressEvent {
	file, err := os.Open(bugProgressPath(root, recordID))
	if err != nil {
		return nil
	}
	defer file.Close()
	var events []bugProgressEvent
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		var event bugProgressEvent
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Stage != "" && event.State != "" {
			events = append(events, event)
		}
	}
	if len(events) > 120 {
		events = events[len(events)-120:]
	}
	return events
}

type agentArtifactMeta struct {
	CWD      string  `json:"cwd"`
	Engine   string  `json:"engine"`
	Return   int     `json:"return"`
	Duration float64 `json:"duration"`
	Time     string  `json:"time"`
}

func readBugArtifactEvents(root, recordID string) []bugProgressEvent {
	dirs, err := os.ReadDir(filepath.Join(root, "agent-runs"))
	if err != nil {
		return nil
	}
	marker := "BUG-" + recordID
	var events []bugProgressEvent
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		base := filepath.Join(root, "agent-runs", dir.Name())
		metaBody, err := os.ReadFile(filepath.Join(base, "meta.json"))
		if err != nil {
			continue
		}
		var meta agentArtifactMeta
		if json.Unmarshal(metaBody, &meta) != nil {
			continue
		}
		cleanCWD := filepath.Clean(meta.CWD)
		if filepath.Base(cleanCWD) != marker && !strings.Contains(filepath.ToSlash(cleanCWD), "/"+marker+"/") {
			continue
		}
		prompt, _ := os.ReadFile(filepath.Join(base, "prompt.md"))
		stage := classifyBugPrompt(string(prompt))
		if stage == "" {
			continue
		}
		result, _ := os.ReadFile(filepath.Join(base, "result.txt"))
		detail := trunc(strings.TrimSpace(string(result)), 12000)
		state := artifactEventState(stage, meta.Return, detail)
		ended, err := time.Parse("2006-01-02 15:04:05 -0700", meta.Time)
		if err != nil {
			if info, statErr := dir.Info(); statErr == nil {
				ended = info.ModTime()
			} else {
				ended = time.Now()
			}
		}
		events = append(events, bugProgressEvent{
			Stage: stage, State: state, Agent: meta.Engine, Detail: detail,
			TS: float64(ended.UnixNano()) / 1e9, Time: ended.Format(time.RFC3339),
		})
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].TS < events[j].TS })
	fixIteration := 0
	for i := range events {
		if events[i].Stage == "fix" {
			fixIteration++
			events[i].Iteration = fixIteration
		} else if (events[i].Stage == "test" || events[i].Stage == "review") && fixIteration > 0 {
			events[i].Iteration = fixIteration
		}
	}
	return events
}

func classifyBugPrompt(prompt string) string {
	switch {
	case strings.Contains(prompt, "Bug 调查 Agent"):
		return "investigate"
	case strings.Contains(prompt, "Bug 修复 Agent"):
		return "fix"
	case strings.Contains(prompt, "独立 Reviewer"):
		return "review"
	default:
		return ""
	}
}

func artifactEventState(stage string, returnCode int, detail string) string {
	if returnCode != 0 {
		return "failed"
	}
	first := strings.ToUpper(strings.TrimSpace(strings.SplitN(detail, "\n", 2)[0]))
	if first == "NEEDS_INPUT" {
		return "waiting"
	}
	if first == "BLOCKED" || first == "FAIL" {
		return "failed"
	}
	return "done"
}

func pipelineSteps(rec *Record) []webPipelineStep {
	fix, review := resolveBugAgents(rec)
	return []webPipelineStep{
		{Stage: "prepare", Label: "准备隔离分支", State: "pending", Actor: "Niuma"},
		{Stage: "investigate", Label: "AI 调查", State: "pending", Actor: fix},
		{Stage: "fix", Label: "AI 修复", State: "pending", Actor: fix},
		{Stage: "test", Label: "自动验证", State: "pending", Actor: "Niuma"},
		{Stage: "review", Label: "独立 Review", State: "pending", Actor: review},
		{Stage: "delivery", Label: "交付人工合并", State: "pending", Actor: "Niuma"},
	}
}

func webPipelineFromRecord(root string, rec *Record) webBugPipeline {
	events := readBugProgress(root, rec.RecordID)
	if len(events) == 0 {
		events = readBugArtifactEvents(root, rec.RecordID)
	}
	steps := pipelineSteps(rec)
	for _, event := range events {
		if event.Stage == "pipeline" && event.State == "failed" {
			markPipelineFailure(steps, event.Detail)
			continue
		}
		for i := range steps {
			if steps[i].Stage != event.Stage {
				continue
			}
			steps[i].State = event.State
			if event.Agent != "" {
				steps[i].Actor = event.Agent
			}
			if event.Detail != "" {
				steps[i].Detail = event.Detail
			}
			break
		}
	}
	status := fieldText(rec.Fields[FStatus])
	normalizePipelineSteps(steps, status, fieldText(rec.Fields[FLog]))
	updatedAt := ""
	if len(events) > 0 {
		updatedAt = events[len(events)-1].Time
	}
	live := status == SBug || status == SReview
	for _, step := range steps {
		if step.State == "running" {
			live = true
		}
	}
	return webBugPipeline{
		BugID: rec.RecordID, Status: status, Live: live, UpdatedAt: updatedAt,
		ServerTime: time.Now().Format(time.RFC3339), Steps: steps, Events: events,
	}
}

func markPipelineFailure(steps []webPipelineStep, detail string) {
	for i := range steps {
		if steps[i].State == "running" {
			steps[i].State = "failed"
			steps[i].Detail = detail
			return
		}
	}
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].State == "failed" {
			if detail != "" {
				steps[i].Detail = detail
			}
			return
		}
	}
	markFirstPipelineStep(steps, "failed", detail)
}

func normalizePipelineSteps(steps []webPipelineStep, status, logText string) {
	setDone := func(index int) {
		if steps[index].State == "pending" {
			steps[index].State = "done"
		}
	}
	switch status {
	case SAnswer:
		setDone(0)
		if steps[1].State == "pending" || steps[1].State == "running" {
			steps[1].State = "waiting"
			steps[1].Detail = "AI 正在等待人工补充信息"
		}
	case SBug, SReview:
		setDone(0)
		if !hasPipelineState(steps, "running") {
			markFirstPipelineStep(steps, "running", "流水线正在执行")
		}
	case SMerge:
		for i := 0; i < 5; i++ {
			steps[i].State = "done"
		}
		steps[5].State = "waiting"
		steps[5].Detail = "修复已准备好，等待人工检查并合并"
	case SDone:
		for i := range steps {
			steps[i].State = "done"
		}
	case SBlocked:
		setDone(0)
		if !hasPipelineState(steps, "failed") {
			markFirstPipelineStep(steps, "failed", tail(strings.TrimSpace(logText), 1200))
		}
	}
}

func hasPipelineState(steps []webPipelineStep, state string) bool {
	for _, step := range steps {
		if step.State == state {
			return true
		}
	}
	return false
}

func markFirstPipelineStep(steps []webPipelineStep, state, detail string) {
	for i := range steps {
		if steps[i].State == "running" || steps[i].State == "pending" {
			steps[i].State = state
			steps[i].Detail = detail
			return
		}
	}
	if len(steps) > 0 {
		steps[len(steps)-1].State = state
		steps[len(steps)-1].Detail = detail
	}
}
