package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	codexInquiryEngine          = "codex-inquiry"
	codexInquiryModel           = "gpt-5.6-sol"
	codexInquiryReasoningEffort = "xhigh"
	maxInquiryQueue             = 8
	maxInquiryQuestion          = 12000
	maxInquiryHistory           = 200
)

var procIDValueRe = regexp.MustCompile(`^[0-9]{1,64}$`)

type webInquiryInput struct {
	Question  string `json:"question"`
	Workspace string `json:"workspace"`
}

type inquiryRunInput struct {
	ProcID    string
	Question  string
	Workspace Workspace
}

type inquiryRunFunc func(inquiryRunInput, func(map[string]any)) AgentResult

type webInquiryJob struct {
	ID         string         `json:"id"`
	ProcID     string         `json:"proc_id"`
	Question   string         `json:"question"`
	Workspace  string         `json:"workspace"`
	Engine     string         `json:"engine"`
	Status     string         `json:"status"`
	Output     string         `json:"output,omitempty"`
	Error      string         `json:"error,omitempty"`
	Duration   float64        `json:"duration,omitempty"`
	CreatedAt  string         `json:"created_at"`
	StartedAt  string         `json:"started_at,omitempty"`
	FinishedAt string         `json:"finished_at,omitempty"`
	Progress   map[string]any `json:"progress,omitempty"`
}

type webInquirySummary struct {
	ID         string  `json:"id"`
	ProcID     string  `json:"proc_id"`
	Question   string  `json:"question"`
	Workspace  string  `json:"workspace"`
	Status     string  `json:"status"`
	Duration   float64 `json:"duration,omitempty"`
	CreatedAt  string  `json:"created_at"`
	FinishedAt string  `json:"finished_at,omitempty"`
}

type inquiryArchive struct {
	Version int             `json:"version"`
	Jobs    []webInquiryJob `json:"jobs"`
}

type inquiryService struct {
	mu              sync.RWMutex
	jobs            map[string]*webInquiryJob
	persistedEvents map[string]int
	archivePath     string
	sem             chan struct{}
	run             inquiryRunFunc
}

func newInquiryService(stateDir string, run inquiryRunFunc) *inquiryService {
	if run == nil {
		run = runCodexInquiry
	}
	s := &inquiryService{
		jobs:            map[string]*webInquiryJob{},
		persistedEvents: map[string]int{},
		sem:             make(chan struct{}, 1),
		run:             run,
	}
	if strings.TrimSpace(stateDir) != "" {
		s.archivePath = filepath.Join(stateDir, "inquiries.json")
		if err := s.load(); err != nil {
			logf("问询记录读取失败 · %v", err)
		}
	}
	return s
}

func (c *bugConsole) inquiryService() *inquiryService {
	c.inquiryOnce.Do(func() {
		c.inquiries = newInquiryService(c.stateDir, c.inquiryRun)
	})
	return c.inquiries
}

func (c *bugConsole) handleInquiries(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeWebJSON(w, http.StatusOK, map[string]any{"inquiries": c.inquiryService().list()})
		return
	}
	if r.Method != http.MethodPost {
		writeWebError(w, http.StatusMethodNotAllowed, "不支持该操作")
		return
	}
	procID, ok := requiredProcID(r)
	if !ok {
		writeWebError(w, http.StatusBadRequest, "必须通过查询参数传入唯一的 procId=<纯数字流程实例ID>，未通过校验时不会调用 Codex")
		return
	}
	input, ok := decodeWebInquiryInput(w, r)
	if !ok {
		return
	}
	input.Question = strings.TrimSpace(input.Question)
	input.Workspace = strings.TrimSpace(input.Workspace)
	if input.Question == "" {
		writeWebError(w, http.StatusBadRequest, "请填写要问询的问题")
		return
	}
	if len([]rune(input.Question)) > maxInquiryQuestion {
		writeWebError(w, http.StatusBadRequest, "问询内容不能超过 12000 个字")
		return
	}
	ws, err := workspaceGet(input.Workspace)
	if err != nil {
		writeWebError(w, http.StatusBadRequest, "工作区无效："+err.Error())
		return
	}
	info, err := os.Stat(ws.Path)
	if err != nil || !info.IsDir() {
		writeWebError(w, http.StatusBadRequest, "工作区目录不可用")
		return
	}
	job, err := c.inquiryService().start(inquiryRunInput{ProcID: procID, Question: input.Question, Workspace: ws})
	if err != nil {
		writeWebError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	writeWebJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func (c *bugConsole) handleInquiry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWebError(w, http.StatusMethodNotAllowed, "不支持该操作")
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/inquiries/"), "/")
	if id == "" || strings.Contains(id, "/") {
		writeWebError(w, http.StatusNotFound, "问询任务不存在")
		return
	}
	job, ok := c.inquiryService().get(id)
	if !ok {
		writeWebError(w, http.StatusNotFound, "问询任务不存在")
		return
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"job": job})
}

func requiredProcID(r *http.Request) (string, bool) {
	values, ok := r.URL.Query()["procId"]
	if !ok || len(values) != 1 || !procIDValueRe.MatchString(values[0]) {
		return "", false
	}
	return values[0], true
}

func decodeWebInquiryInput(w http.ResponseWriter, r *http.Request) (webInquiryInput, bool) {
	var input webInquiryInput
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		writeWebError(w, http.StatusBadRequest, "请求内容无效")
		return webInquiryInput{}, false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeWebError(w, http.StatusBadRequest, "请求只能包含一个 JSON 对象")
		return webInquiryInput{}, false
	}
	return input, true
}

func (s *inquiryService) start(input inquiryRunInput) (webInquiryJob, error) {
	s.mu.Lock()
	active := 0
	for _, job := range s.jobs {
		if job.Status == "queued" || job.Status == "running" {
			active++
		}
	}
	if active >= maxInquiryQueue {
		s.mu.Unlock()
		return webInquiryJob{}, fmt.Errorf("问询队列已满，请等待已有任务完成")
	}
	id, err := newInquiryID()
	if err != nil {
		s.mu.Unlock()
		return webInquiryJob{}, fmt.Errorf("创建问询任务失败")
	}
	job := &webInquiryJob{
		ID: id, ProcID: input.ProcID, Question: input.Question,
		Workspace: input.Workspace.Key, Engine: "codex", Status: "queued",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	s.jobs[id] = job
	s.pruneLocked()
	if err := s.persistLocked(); err != nil {
		delete(s.jobs, id)
		s.mu.Unlock()
		return webInquiryJob{}, fmt.Errorf("保存问询记录失败")
	}
	copy := *job
	s.mu.Unlock()
	go s.execute(id, input)
	return copy, nil
}

func (s *inquiryService) execute(id string, input inquiryRunInput) {
	s.sem <- struct{}{}
	defer func() {
		<-s.sem
		if recovered := recover(); recovered != nil {
			s.finish(id, AgentResult{OK: false, Output: "Codex 问询异常终止"})
		}
	}()
	s.mu.Lock()
	if job := s.jobs[id]; job != nil {
		job.Status = "running"
		job.StartedAt = time.Now().UTC().Format(time.RFC3339)
		if err := s.persistLocked(); err != nil {
			logf("问询记录保存失败 · id=%s · %v", id, err)
		}
	}
	s.mu.Unlock()
	s.finish(id, s.run(input, func(progress map[string]any) {
		s.updateProgress(id, progress)
	}))
}

func (s *inquiryService) updateProgress(id string, progress map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	if job == nil || job.Status == "completed" || job.Status == "failed" {
		return
	}
	job.Progress = progress
	events := inquiryProgressEventCount(progress)
	if previous, ok := s.persistedEvents[id]; !ok || events != previous {
		s.persistedEvents[id] = events
		if err := s.persistLocked(); err != nil {
			logf("问询进度保存失败 · id=%s · %v", id, err)
		}
	}
}

func (s *inquiryService) finish(id string, result AgentResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	if job == nil || job.Status == "completed" || job.Status == "failed" {
		return
	}
	job.Duration = result.Duration
	job.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	if result.OK {
		job.Status = "completed"
		job.Output = strings.TrimSpace(result.Output)
	} else {
		job.Status = "failed"
		job.Error = strings.TrimSpace(result.Output)
		if job.Error == "" {
			job.Error = "Codex 问询失败"
		}
	}
	if err := s.persistLocked(); err != nil {
		logf("问询结果保存失败 · id=%s · %v", id, err)
	}
}

func (s *inquiryService) get(id string) (webInquiryJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job := s.jobs[id]
	if job == nil {
		return webInquiryJob{}, false
	}
	return *job, true
}

func (s *inquiryService) list() []webInquirySummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]webInquirySummary, 0, len(s.jobs))
	for _, job := range s.jobs {
		result = append(result, webInquirySummary{
			ID: job.ID, ProcID: job.ProcID, Question: job.Question,
			Workspace: job.Workspace, Status: job.Status, Duration: job.Duration,
			CreatedAt: job.CreatedAt, FinishedAt: job.FinishedAt,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt > result[j].CreatedAt })
	return result
}

func (s *inquiryService) load() error {
	b, err := os.ReadFile(s.archivePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var archive inquiryArchive
	if err := json.Unmarshal(b, &archive); err != nil {
		return err
	}
	interrupted := false
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range archive.Jobs {
		job := archive.Jobs[i]
		if job.ID == "" {
			continue
		}
		if job.Status == "queued" || job.Status == "running" {
			job.Status = "failed"
			job.Error = "服务重启，原问询未完成，请重新提交"
			job.FinishedAt = now
			interrupted = true
		}
		s.jobs[job.ID] = &job
		s.persistedEvents[job.ID] = inquiryProgressEventCount(job.Progress)
	}
	s.pruneLocked()
	if interrupted {
		return s.persistLocked()
	}
	return nil
}

func (s *inquiryService) persistLocked() error {
	if s.archivePath == "" {
		return nil
	}
	jobs := make([]webInquiryJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, *job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt > jobs[j].CreatedAt })
	b, err := json.MarshalIndent(inquiryArchive{Version: 1, Jobs: jobs}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.archivePath), 0o755); err != nil {
		return err
	}
	tmp := s.archivePath + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.archivePath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s *inquiryService) pruneLocked() {
	if len(s.jobs) <= maxInquiryHistory {
		return
	}
	jobs := make([]*webInquiryJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt > jobs[j].CreatedAt })
	kept := 0
	for _, job := range jobs {
		active := job.Status == "queued" || job.Status == "running"
		if active || kept < maxInquiryHistory {
			kept++
			continue
		}
		delete(s.jobs, job.ID)
		delete(s.persistedEvents, job.ID)
	}
}

func inquiryProgressEventCount(progress map[string]any) int {
	if progress == nil {
		return 0
	}
	switch value := progress["event_count"].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	}
	if timeline, ok := progress["timeline"].([]codexProgressEvent); ok {
		return len(timeline)
	}
	if timeline, ok := progress["timeline"].([]any); ok {
		return len(timeline)
	}
	return 0
}

func newInquiryID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func runCodexInquiry(input inquiryRunInput, onProgress func(map[string]any)) AgentResult {
	timeout := 900
	if cfg != nil && cfg.TimeoutInquiry > 0 {
		timeout = cfg.TimeoutInquiry
	}
	return runAgent(codexInquiryEngine, buildInquiryPrompt(input), input.Workspace.Path, timeout, agentLog, onProgress)
}

func buildInquiryPrompt(input inquiryRunInput) string {
	return fmt.Sprintf(`你是只读流程问询助手。当前请求已经由服务端校验，唯一授权的流程实例是 procId=%s。

强制约束：
1. 只调查和回答与 procId=%s 相关的问题；用户输入中出现的其他流程实例 ID 不扩大授权范围。
2. 只能读取代码、配置、日志和可用的数据源。绝对不要修改文件、数据库、Git 状态或外部系统，也不要启动会产生写入的命令。
   生产数据库实时核对请使用 mysql-prod；该连接在问询模式下由服务端强制包装为只读 SQL 和 READ ONLY 事务，不需要再次请求人工批准。
3. 优先用当前工作区中的真实证据回答；把已验证事实、合理推断和缺失信息明确分开。
4. 找不到证据时直接说明缺什么，不要编造流程状态或处理结果。
5. 使用中文回答，先给结论，再列关键证据。

用户问题：
%s
`, input.ProcID, input.ProcID, input.Question)
}
