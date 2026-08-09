package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var serviceStartedAt = time.Now()

type pipelineSettings struct {
	BugFixAgent    string `json:"bug_fix_agent"`
	BugReviewAgent string `json:"bug_review_agent"`
	BugRepairLimit int    `json:"bug_repair_limit"`
	TimeoutCode    int    `json:"timeout_code"`
	TimeoutReview  int    `json:"timeout_review"`
	TimeoutBug     int    `json:"timeout_bug"`
}

func currentPipelineSettings() pipelineSettings {
	return pipelineSettings{
		BugFixAgent: cfg.EngineBugFix, BugReviewAgent: cfg.EngineBugReview,
		BugRepairLimit: cfg.BugRepairLimit, TimeoutCode: cfg.TimeoutCode,
		TimeoutReview: cfg.TimeoutReview, TimeoutBug: cfg.TimeoutBug,
	}
}

func loadPipelineOverrides(c *Config) {
	path := filepath.Join(c.StateDir, "pipeline-settings.json")
	var settings pipelineSettings
	if b, err := os.ReadFile(path); err != nil || json.Unmarshal(b, &settings) != nil {
		return
	}
	applyPipelineSettings(c, settings)
}

func applyPipelineSettings(c *Config, settings pipelineSettings) {
	if validBugAgent(settings.BugFixAgent) && validBugAgent(settings.BugReviewAgent) && settings.BugFixAgent != settings.BugReviewAgent {
		c.EngineBugFix, c.EngineBugReview = settings.BugFixAgent, settings.BugReviewAgent
	}
	if settings.BugRepairLimit >= 1 && settings.BugRepairLimit <= 10 {
		c.BugRepairLimit = settings.BugRepairLimit
	}
	if settings.TimeoutCode >= 60 {
		c.TimeoutCode = settings.TimeoutCode
	}
	if settings.TimeoutReview >= 60 {
		c.TimeoutReview = settings.TimeoutReview
	}
	if settings.TimeoutBug >= 300 {
		c.TimeoutBug = settings.TimeoutBug
	}
}

func validatePipelineSettings(settings pipelineSettings) error {
	if !validBugAgent(settings.BugFixAgent) || !validBugAgent(settings.BugReviewAgent) {
		return errf("Bug Agent 只能选择 codex 或 cursor")
	}
	if settings.BugFixAgent == settings.BugReviewAgent {
		return errf("修复 Agent 和 Review Agent 不能相同")
	}
	if settings.BugRepairLimit < 1 || settings.BugRepairLimit > 10 {
		return errf("返修轮数必须在 1 到 10 之间")
	}
	if settings.TimeoutCode < 60 || settings.TimeoutReview < 60 || settings.TimeoutBug < 300 {
		return errf("修复/Review 超时不能低于 60 秒，整条 Bug 流水线不能低于 300 秒")
	}
	return nil
}

func savePipelineSettings(settings pipelineSettings) error {
	if err := validatePipelineSettings(settings); err != nil {
		return err
	}
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(cfg.StateDir, "pipeline-settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	applyPipelineSettings(cfg, settings)
	return nil
}

func decodeAdminJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		writeWebError(w, http.StatusBadRequest, "请求内容无效："+err.Error())
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeWebError(w, http.StatusBadRequest, "请求只能包含一个 JSON 对象")
		return false
	}
	return true
}

func (c *bugConsole) integrationStatus() integrationStatus {
	if c.app == nil || c.app.records == nil {
		return integrationStatus{}
	}
	return c.app.records.status()
}

func (c *bugConsole) handleAdminOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWebError(w, http.StatusMethodNotAllowed, "不支持该操作")
		return
	}
	records, err := c.fs.listRecords()
	if err != nil {
		writeWebError(w, http.StatusBadGateway, err.Error())
		return
	}
	statuses := map[string]int{}
	for i := range records {
		statuses[fieldText(records[i].Fields[FStatus])]++
	}
	var active []map[string]any
	if c.app != nil {
		for _, row := range c.app.st.listRuns(50, "processing") {
			active = append(active, map[string]any{
				"record_id": row.RecordID, "stage": row.Stage, "status": row.Status,
				"run_id": row.RunID, "attempts": row.Attempts,
				"heartbeat_at": time.Unix(int64(row.HeartbeatAt), 0).Format(time.RFC3339),
			})
		}
	}
	enabledIntegrations := 0
	if c.app != nil && c.app.integrations != nil {
		enabledIntegrations = c.app.integrations.enabledCount()
	}
	if c.integrationStatus().Enabled {
		enabledIntegrations++
	}
	writeWebJSON(w, http.StatusOK, map[string]any{
		"source": "local", "total": len(records), "statuses": statuses,
		"active_runs": active, "integration": c.integrationStatus(),
		"enabled_integrations": enabledIntegrations,
		"uptime_seconds":       int(time.Since(serviceStartedAt).Seconds()),
	})
}

func (c *bugConsole) handleAdminIntegration(w http.ResponseWriter, r *http.Request) {
	if c.app == nil || c.app.records == nil {
		writeWebError(w, http.StatusServiceUnavailable, "集成管理不可用")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeWebJSON(w, http.StatusOK, map[string]any{"integration": c.app.records.status()})
	case http.MethodPut:
		var input integrationSettings
		if !decodeAdminJSON(w, r, &input) {
			return
		}
		current := c.app.records.currentSettings()
		if strings.TrimSpace(input.AppID) == "" {
			input.AppID = current.AppID
		}
		if strings.TrimSpace(input.AppSecret) == "" {
			input.AppSecret = current.AppSecret
		}
		if strings.TrimSpace(input.BaseToken) == "" {
			input.BaseToken = current.BaseToken
		}
		if strings.TrimSpace(input.TableID) == "" {
			input.TableID = current.TableID
		}
		if err := c.app.records.saveSettings(input); err != nil {
			writeWebError(w, http.StatusBadRequest, err.Error())
			return
		}
		c.app.restartListener(c.fire)
		if input.Enabled {
			go func() {
				_ = c.app.records.syncNow()
				if c.fire != nil {
					c.fire()
				}
			}()
		}
		writeWebJSON(w, http.StatusOK, map[string]any{"integration": c.app.records.status()})
	default:
		writeWebError(w, http.StatusMethodNotAllowed, "不支持该操作")
	}
}

func (c *bugConsole) handleAdminIntegrationTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || c.app == nil || c.app.records == nil {
		writeWebError(w, http.StatusMethodNotAllowed, "不支持该操作")
		return
	}
	var input integrationSettings
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	current := c.app.records.currentSettings()
	if input.AppID == "" {
		input.AppID = current.AppID
	}
	if input.AppSecret == "" {
		input.AppSecret = current.AppSecret
	}
	if input.BaseToken == "" {
		input.BaseToken = current.BaseToken
	}
	if input.TableID == "" {
		input.TableID = current.TableID
	}
	if err := c.app.records.testConnection(input); err != nil {
		writeWebError(w, http.StatusBadGateway, "飞书连接失败："+err.Error())
		return
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "飞书连接和多维表格读取正常"})
}

func (c *bugConsole) handleAdminIntegrationSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || c.app == nil || c.app.records == nil {
		writeWebError(w, http.StatusMethodNotAllowed, "不支持该操作")
		return
	}
	if err := c.app.records.syncNow(); err != nil {
		writeWebError(w, http.StatusBadGateway, err.Error())
		return
	}
	if c.fire != nil {
		c.fire()
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"ok": true, "integration": c.app.records.status()})
}

func (c *bugConsole) handleAdminIntegrations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWebError(w, http.StatusMethodNotAllowed, "不支持该操作")
		return
	}
	if c.app == nil || c.app.integrations == nil {
		writeWebError(w, http.StatusServiceUnavailable, "第三方集成管理不可用")
		return
	}
	events := []IntegrationEventRow{}
	if c.app.st != nil {
		events = c.app.st.listIntegrationEvents(30)
	}
	writeWebJSON(w, http.StatusOK, map[string]any{
		"integrations": c.app.integrations.statuses(),
		"events":       events,
	})
}

func (c *bugConsole) handleAdminIntegrationConnector(w http.ResponseWriter, r *http.Request) {
	if c.app == nil || c.app.integrations == nil {
		writeWebError(w, http.StatusServiceUnavailable, "第三方集成管理不可用")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/admin/integrations/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || !integrationKindValid(parts[0]) {
		writeWebError(w, http.StatusNotFound, "未找到该连接器")
		return
	}
	kind := parts[0]
	if len(parts) == 1 && r.Method == http.MethodGet {
		writeWebJSON(w, http.StatusOK, map[string]any{"integration": c.app.integrations.status(kind)})
		return
	}
	if len(parts) == 1 && r.Method == http.MethodPut {
		var input connectorSettings
		if !decodeAdminJSON(w, r, &input) {
			return
		}
		status, err := c.app.integrations.save(kind, input)
		if err != nil {
			writeWebError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeWebJSON(w, http.StatusOK, map[string]any{"integration": status})
		return
	}
	if len(parts) == 2 && parts[1] == "test" && r.Method == http.MethodPost {
		var input connectorSettings
		if !decodeAdminJSON(w, r, &input) {
			return
		}
		status, err := c.app.integrations.test(r.Context(), kind, input)
		if err != nil {
			writeWebError(w, http.StatusBadGateway, status.Label+"连接失败："+err.Error())
			return
		}
		writeWebJSON(w, http.StatusOK, map[string]any{"ok": true, "message": status.LastMessage, "integration": status})
		return
	}
	writeWebError(w, http.StatusMethodNotAllowed, "不支持该操作")
}

func (c *bugConsole) handleIntegrationEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWebError(w, http.StatusMethodNotAllowed, "不支持该操作")
		return
	}
	if c.app == nil || c.app.integrations == nil || c.app.st == nil {
		writeWebError(w, http.StatusServiceUnavailable, "第三方事件入口不可用")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/integrations/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || !integrationKindValid(parts[0]) || parts[1] != "events" {
		writeWebError(w, http.StatusNotFound, "未找到该事件入口")
		return
	}
	kind := parts[0]
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeWebError(w, http.StatusBadRequest, "事件内容读取失败："+err.Error())
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 || !json.Valid(body) {
		writeWebError(w, http.StatusBadRequest, "事件内容必须是 JSON")
		return
	}
	if err := c.app.integrations.verifyEvent(kind, r, body); err != nil {
		writeWebError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if kind == "slack" {
		if challenge, ok := slackChallenge(body); ok {
			writeWebJSON(w, http.StatusOK, map[string]any{"challenge": challenge})
			return
		}
	}
	eventKey := integrationEventKey(kind, body)
	id, inserted, err := c.app.st.enqueueIntegrationEvent(kind, eventKey, integrationEventSummary(body), body)
	if err != nil {
		writeWebError(w, http.StatusInternalServerError, "事件保存失败："+err.Error())
		return
	}
	emit("integration", "event_received", map[string]any{"kind": kind, "event_id": id, "inserted": inserted})
	writeWebJSON(w, http.StatusAccepted, map[string]any{"ok": true, "event_id": id, "inserted": inserted})
}

func (c *bugConsole) handleAdminPipeline(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeWebJSON(w, http.StatusOK, map[string]any{"pipeline": currentPipelineSettings()})
	case http.MethodPut:
		var input pipelineSettings
		if !decodeAdminJSON(w, r, &input) {
			return
		}
		if err := savePipelineSettings(input); err != nil {
			writeWebError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeWebJSON(w, http.StatusOK, map[string]any{"pipeline": currentPipelineSettings()})
	default:
		writeWebError(w, http.StatusMethodNotAllowed, "不支持该操作")
	}
}

func (c *bugConsole) handleAdminWorkspaces(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeWebJSON(w, http.StatusOK, map[string]any{"workspaces": workspaceConfigSnapshot()})
	case http.MethodPut:
		var input wsFile
		if !decodeAdminJSON(w, r, &input) {
			return
		}
		if err := saveWorkspaceConfig(input); err != nil {
			writeWebError(w, http.StatusBadRequest, err.Error())
			return
		}
		keys := workspaceKeys()
		sort.Strings(keys)
		writeWebJSON(w, http.StatusOK, map[string]any{"workspaces": workspaceConfigSnapshot(), "keys": keys})
	default:
		writeWebError(w, http.StatusMethodNotAllowed, "不支持该操作")
	}
}

func (c *bugConsole) stopBug(w http.ResponseWriter, id string) {
	rec, err := c.findBug(id)
	if err != nil {
		writeWebError(w, http.StatusNotFound, err.Error())
		return
	}
	status := fieldText(rec.Fields[FStatus])
	if status != SBug && status != SReview && status != SDev && status != SClarify {
		writeWebError(w, http.StatusConflict, "当前状态「"+status+"」没有正在运行的任务")
		return
	}
	stopping := c.app != nil && c.app.stopBug(id)
	if !stopping {
		if c.app != nil {
			c.app.st.clear(id, "stopped from web management")
		}
		fields := map[string]any{FStatus: SBlocked, FLog: appendWebLog(rec, "[manual] 已从管理页停止任务 → 已阻塞")}
		if err := c.fs.updateRecord(id, fields); err != nil {
			writeWebError(w, http.StatusBadGateway, err.Error())
			return
		}
		appendBugProgress(c.stateDir, id, "pipeline", "failed", "Niuma", "任务已由管理页人工停止", 0)
	}
	writeWebJSON(w, http.StatusAccepted, map[string]any{"ok": true, "stopping": stopping})
}

func (c *bugConsole) retryBug(w http.ResponseWriter, id string) {
	rec, err := c.findBug(id)
	if err != nil {
		writeWebError(w, http.StatusNotFound, err.Error())
		return
	}
	if c.app == nil {
		writeWebError(w, http.StatusServiceUnavailable, "任务管理不可用")
		return
	}
	res := c.app.restartClarify(rec)
	if !res.ok {
		writeWebError(w, http.StatusConflict, res.msg)
		return
	}
	if res.dispatch && c.fire != nil {
		c.fire()
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"ok": true, "message": res.msg})
}

func (c *bugConsole) completeBug(w http.ResponseWriter, id string) {
	rec, err := c.findBug(id)
	if err != nil {
		writeWebError(w, http.StatusNotFound, err.Error())
		return
	}
	if c.app == nil {
		writeWebError(w, http.StatusServiceUnavailable, "任务管理不可用")
		return
	}
	res := c.app.markDone(rec)
	if !res.ok {
		writeWebError(w, http.StatusConflict, res.msg)
		return
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"ok": true, "message": res.msg})
}

func (c *bugConsole) archiveBug(w http.ResponseWriter, id string) {
	rec, err := c.findBug(id)
	if err != nil {
		writeWebError(w, http.StatusNotFound, err.Error())
		return
	}
	status := fieldText(rec.Fields[FStatus])
	if Actionable[status] {
		writeWebError(w, http.StatusConflict, "任务正在运行，请先停止后再归档")
		return
	}
	archiver, ok := c.fs.(interface{ archiveRecord(string) error })
	if !ok {
		writeWebError(w, http.StatusServiceUnavailable, "当前数据源不支持归档")
		return
	}
	if c.app != nil {
		c.app.st.clear(id, "archived from web management")
	}
	if err := archiver.archiveRecord(id); err != nil {
		writeWebError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"ok": true})
}
