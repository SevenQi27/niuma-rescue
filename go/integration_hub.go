package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const integrationConfigVersion = 1

var integrationKinds = []string{"zentao", "jira", "slack"}

type connectorSettings struct {
	Enabled        bool   `json:"enabled"`
	Name           string `json:"name"`
	BaseURL        string `json:"base_url"`
	Mode           string `json:"mode"`
	Deployment     string `json:"deployment"`
	APIPath        string `json:"api_path"`
	APIUser        string `json:"api_user"`
	APIToken       string `json:"api_token"`
	AppToken       string `json:"app_token"`
	SigningSecret  string `json:"signing_secret"`
	WebhookSecret  string `json:"webhook_secret"`
	Project        string `json:"project"`
	TriggerFilter  string `json:"trigger_filter"`
	Workspace      string `json:"workspace"`
	DefaultChannel string `json:"default_channel"`
}

type connectorRuntime struct {
	LastTestAt    time.Time
	LastTestError string
	LastMessage   string
}

type connectorStatus struct {
	Kind             string   `json:"kind"`
	Label            string   `json:"label"`
	Icon             string   `json:"icon"`
	Description      string   `json:"description"`
	Enabled          bool     `json:"enabled"`
	Configured       bool     `json:"configured"`
	Name             string   `json:"name"`
	BaseURL          string   `json:"base_url"`
	Mode             string   `json:"mode"`
	Deployment       string   `json:"deployment"`
	APIPath          string   `json:"api_path"`
	APIUser          string   `json:"api_user"`
	HasAPIToken      bool     `json:"has_api_token"`
	HasAppToken      bool     `json:"has_app_token"`
	HasSigningSecret bool     `json:"has_signing_secret"`
	HasWebhookSecret bool     `json:"has_webhook_secret"`
	Project          string   `json:"project"`
	TriggerFilter    string   `json:"trigger_filter"`
	Workspace        string   `json:"workspace"`
	DefaultChannel   string   `json:"default_channel"`
	LastTestAt       string   `json:"last_test_at"`
	LastTestError    string   `json:"last_test_error"`
	LastMessage      string   `json:"last_message"`
	WebhookPath      string   `json:"webhook_path"`
	Capabilities     []string `json:"capabilities"`
}

type integrationConfigFile struct {
	Version int                          `json:"version"`
	Items   map[string]connectorSettings `json:"items"`
}

type integrationHub struct {
	path    string
	client  *http.Client
	mu      sync.RWMutex
	items   map[string]connectorSettings
	runtime map[string]connectorRuntime
}

func newIntegrationHub(stateDir string) *integrationHub {
	h := &integrationHub{
		path:   filepath.Join(stateDir, "integrations.json"),
		client: &http.Client{Timeout: 12 * time.Second},
		items:  make(map[string]connectorSettings), runtime: make(map[string]connectorRuntime),
	}
	h.load()
	return h
}

func (h *integrationHub) load() {
	var saved integrationConfigFile
	if b, err := os.ReadFile(h.path); err == nil && json.Unmarshal(b, &saved) == nil {
		for kind, settings := range saved.Items {
			if integrationKindValid(kind) {
				h.items[kind] = normalizeConnectorSettings(kind, settings)
			}
		}
	}
	for _, kind := range integrationKinds {
		if _, ok := h.items[kind]; !ok {
			h.items[kind] = normalizeConnectorSettings(kind, connectorSettings{})
		}
	}
}

func integrationKindValid(kind string) bool {
	for _, candidate := range integrationKinds {
		if kind == candidate {
			return true
		}
	}
	return false
}

func normalizeConnectorSettings(kind string, settings connectorSettings) connectorSettings {
	settings.Name = strings.TrimSpace(settings.Name)
	settings.BaseURL = strings.TrimRight(strings.TrimSpace(settings.BaseURL), "/")
	settings.Mode = strings.ToLower(strings.TrimSpace(settings.Mode))
	settings.Deployment = strings.ToLower(strings.TrimSpace(settings.Deployment))
	settings.APIPath = "/" + strings.TrimLeft(strings.TrimSpace(settings.APIPath), "/")
	settings.APIUser = strings.TrimSpace(settings.APIUser)
	settings.APIToken = strings.TrimSpace(settings.APIToken)
	settings.AppToken = strings.TrimSpace(settings.AppToken)
	settings.SigningSecret = strings.TrimSpace(settings.SigningSecret)
	settings.WebhookSecret = strings.TrimSpace(settings.WebhookSecret)
	settings.Project = strings.TrimSpace(settings.Project)
	settings.TriggerFilter = strings.TrimSpace(settings.TriggerFilter)
	settings.Workspace = strings.TrimSpace(settings.Workspace)
	settings.DefaultChannel = strings.TrimSpace(settings.DefaultChannel)
	switch kind {
	case "zentao":
		if settings.Name == "" {
			settings.Name = "禅道"
		}
		if settings.Mode == "" {
			settings.Mode = "webhook"
		}
		if settings.APIPath == "/" {
			settings.APIPath = "/api.php/v1"
		}
	case "jira":
		if settings.Name == "" {
			settings.Name = "Jira"
		}
		if settings.Mode == "" {
			settings.Mode = "webhook"
		}
		if settings.Deployment == "" {
			settings.Deployment = "cloud"
		}
		if settings.APIPath == "/" {
			settings.APIPath = "/rest/api/3"
		}
	case "slack":
		if settings.Name == "" {
			settings.Name = "Slack"
		}
		if settings.Mode == "" {
			settings.Mode = "socket"
		}
	}
	return settings
}

func connectorConfigured(kind string, settings connectorSettings) bool {
	switch kind {
	case "zentao":
		return settings.BaseURL != "" && settings.APIToken != "" && settings.WebhookSecret != ""
	case "jira":
		return settings.BaseURL != "" && settings.APIUser != "" && settings.APIToken != "" && settings.Project != "" && settings.WebhookSecret != ""
	case "slack":
		if settings.Mode == "webhook" {
			return settings.APIToken != "" && settings.SigningSecret != ""
		}
		return settings.APIToken != "" && settings.AppToken != ""
	}
	return false
}

func validateConnectorSettings(kind string, settings connectorSettings, enabling bool) error {
	if !integrationKindValid(kind) {
		return errf("不支持的集成类型：%s", kind)
	}
	if settings.BaseURL != "" {
		parsed, err := url.Parse(settings.BaseURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errf("服务地址必须是完整的 http 或 https 地址")
		}
	}
	if settings.Workspace != "" {
		if _, err := workspaceGet(settings.Workspace); err != nil {
			return errf("工作区不存在：%s", settings.Workspace)
		}
	}
	if kind == "jira" && settings.Deployment != "cloud" && settings.Deployment != "dc" {
		return errf("Jira 部署类型只能选择 Cloud 或 Data Center")
	}
	if kind == "slack" && settings.Mode != "socket" && settings.Mode != "webhook" {
		return errf("Slack 接入模式只能选择 Socket Mode 或 Events API")
	}
	if enabling && !connectorConfigured(kind, settings) {
		switch kind {
		case "zentao":
			return errf("启用禅道需要填写服务地址、API Token 和 Webhook Secret")
		case "jira":
			return errf("启用 Jira 需要填写服务地址、账号、API Token、项目 Key 和 Webhook Secret")
		case "slack":
			return errf("启用 Slack 需要 Bot Token，并按接入模式填写 App Token 或 Signing Secret")
		}
	}
	return nil
}

func mergeConnectorSecrets(input, current connectorSettings) connectorSettings {
	if input.APIToken == "" {
		input.APIToken = current.APIToken
	}
	if input.AppToken == "" {
		input.AppToken = current.AppToken
	}
	if input.SigningSecret == "" {
		input.SigningSecret = current.SigningSecret
	}
	if input.WebhookSecret == "" {
		input.WebhookSecret = current.WebhookSecret
	}
	return input
}

func (h *integrationHub) current(kind string) (connectorSettings, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	settings, ok := h.items[kind]
	return settings, ok
}

func (h *integrationHub) save(kind string, input connectorSettings) (connectorStatus, error) {
	current, _ := h.current(kind)
	settings := normalizeConnectorSettings(kind, mergeConnectorSecrets(input, current))
	if err := validateConnectorSettings(kind, settings, settings.Enabled); err != nil {
		return connectorStatus{}, err
	}
	h.mu.Lock()
	previous := h.items[kind]
	h.items[kind] = settings
	snapshot := make(map[string]connectorSettings, len(h.items))
	for key, value := range h.items {
		snapshot[key] = value
	}
	b, err := json.MarshalIndent(integrationConfigFile{Version: integrationConfigVersion, Items: snapshot}, "", "  ")
	if err != nil {
		h.items[kind] = previous
		h.mu.Unlock()
		return connectorStatus{}, err
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o755); err != nil {
		h.items[kind] = previous
		h.mu.Unlock()
		return connectorStatus{}, err
	}
	if err := os.WriteFile(h.path, append(b, '\n'), 0o600); err != nil {
		h.items[kind] = previous
		h.mu.Unlock()
		return connectorStatus{}, err
	}
	h.mu.Unlock()
	return h.status(kind), nil
}

func connectorPresentation(kind string) (label, icon, description string, capabilities []string) {
	switch kind {
	case "zentao":
		return "禅道", "禅", "接收 Bug / 需求事件，并为后续状态回写保留绑定。", []string{"Webhook 接收", "API 连通测试", "工作区映射"}
	case "jira":
		return "Jira", "J", "按项目和过滤规则接收 Issue，兼容 Cloud 与 Data Center 配置。", []string{"Webhook 接收", "Issue API 测试", "项目映射"}
	case "slack":
		return "Slack", "S", "作为团队提交入口和进度通知渠道，优先使用 Socket Mode。", []string{"Token 连通测试", "Socket / Webhook 配置", "频道映射"}
	}
	return kind, "?", "第三方连接器", nil
}

func (h *integrationHub) status(kind string) connectorStatus {
	h.mu.RLock()
	settings := h.items[kind]
	runtime := h.runtime[kind]
	h.mu.RUnlock()
	label, icon, description, capabilities := connectorPresentation(kind)
	return connectorStatus{
		Kind: kind, Label: label, Icon: icon, Description: description,
		Enabled: settings.Enabled, Configured: connectorConfigured(kind, settings),
		Name: settings.Name, BaseURL: settings.BaseURL, Mode: settings.Mode,
		Deployment: settings.Deployment, APIPath: settings.APIPath, APIUser: settings.APIUser,
		HasAPIToken: settings.APIToken != "", HasAppToken: settings.AppToken != "",
		HasSigningSecret: settings.SigningSecret != "", HasWebhookSecret: settings.WebhookSecret != "",
		Project: settings.Project, TriggerFilter: settings.TriggerFilter, Workspace: settings.Workspace,
		DefaultChannel: settings.DefaultChannel, LastTestAt: formatOptionalTime(runtime.LastTestAt),
		LastTestError: runtime.LastTestError, LastMessage: runtime.LastMessage,
		WebhookPath: "/api/integrations/" + kind + "/events", Capabilities: capabilities,
	}
}

func (h *integrationHub) statuses() []connectorStatus {
	statuses := make([]connectorStatus, 0, len(integrationKinds))
	for _, kind := range integrationKinds {
		statuses = append(statuses, h.status(kind))
	}
	return statuses
}

func (h *integrationHub) enabledCount() int {
	count := 0
	for _, status := range h.statuses() {
		if status.Enabled {
			count++
		}
	}
	return count
}

func (h *integrationHub) test(ctx context.Context, kind string, input connectorSettings) (connectorStatus, error) {
	current, ok := h.current(kind)
	if !ok {
		return connectorStatus{}, errf("不支持的集成类型：%s", kind)
	}
	settings := normalizeConnectorSettings(kind, mergeConnectorSecrets(input, current))
	if err := validateConnectorSettings(kind, settings, false); err != nil {
		return h.status(kind), err
	}
	message, err := h.probe(ctx, kind, settings)
	h.mu.Lock()
	h.runtime[kind] = connectorRuntime{LastTestAt: time.Now(), LastMessage: message}
	if err != nil {
		h.runtime[kind] = connectorRuntime{LastTestAt: time.Now(), LastTestError: err.Error()}
	}
	h.mu.Unlock()
	return h.status(kind), err
}

func (h *integrationHub) probe(ctx context.Context, kind string, settings connectorSettings) (string, error) {
	switch kind {
	case "zentao":
		if settings.BaseURL == "" || settings.APIToken == "" {
			return "", errf("请先填写禅道服务地址和 API Token")
		}
		endpoint := settings.BaseURL + strings.TrimRight(settings.APIPath, "/") + "/products"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Token", settings.APIToken)
		if settings.APIUser != "" {
			req.Header.Set("Account", settings.APIUser)
		}
		if err := h.expectHTTP(req, http.StatusOK); err != nil {
			return "", err
		}
		return "禅道 API 可访问，产品列表读取正常", nil
	case "jira":
		if settings.BaseURL == "" || settings.APIUser == "" || settings.APIToken == "" {
			return "", errf("请先填写 Jira 服务地址、账号和 API Token")
		}
		apiPath := settings.APIPath
		if settings.Deployment == "dc" && apiPath == "/rest/api/3" {
			apiPath = "/rest/api/2"
		}
		endpoint := settings.BaseURL + strings.TrimRight(apiPath, "/") + "/myself"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return "", err
		}
		req.SetBasicAuth(settings.APIUser, settings.APIToken)
		if err := h.expectHTTP(req, http.StatusOK); err != nil {
			return "", err
		}
		return "Jira API 登录成功，当前账号信息读取正常", nil
	case "slack":
		if settings.APIToken == "" {
			return "", errf("请先填写 Slack Bot Token")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/auth.test", strings.NewReader(""))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+settings.APIToken)
		response, err := h.client.Do(req)
		if err != nil {
			return "", err
		}
		defer response.Body.Close()
		var payload struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
			Team  string `json:"team"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
			return "", errf("Slack 返回内容无效：%v", err)
		}
		if response.StatusCode != http.StatusOK || !payload.OK {
			return "", errf("Slack 鉴权失败：%s", strings.TrimSpace(payload.Error))
		}
		if payload.Team != "" {
			return "Slack Bot 已连接到工作区「" + payload.Team + "」", nil
		}
		return "Slack Bot Token 验证正常", nil
	}
	return "", errf("不支持的集成类型：%s", kind)
}

func (h *integrationHub) expectHTTP(request *http.Request, expected int) error {
	response, err := h.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
	if response.StatusCode != expected {
		message := strings.TrimSpace(string(body))
		if len(message) > 240 {
			message = message[:240]
		}
		if message != "" {
			return errf("远端返回 HTTP %d：%s", response.StatusCode, message)
		}
		return errf("远端返回 HTTP %d", response.StatusCode)
	}
	return nil
}

func (h *integrationHub) verifyEvent(kind string, request *http.Request, body []byte) error {
	settings, ok := h.current(kind)
	if !ok || !settings.Enabled {
		return errf("连接器未启用")
	}
	if kind == "slack" && settings.Mode == "webhook" {
		timestamp := request.Header.Get("X-Slack-Request-Timestamp")
		provided := request.Header.Get("X-Slack-Signature")
		seconds, err := strconv.ParseInt(timestamp, 10, 64)
		if err != nil || time.Since(time.Unix(seconds, 0)) > 5*time.Minute || time.Until(time.Unix(seconds, 0)) > 5*time.Minute {
			return errf("Slack 请求时间戳无效")
		}
		mac := hmac.New(sha256.New, []byte(settings.SigningSecret))
		mac.Write([]byte("v0:" + timestamp + ":"))
		mac.Write(body)
		expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
		if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			return errf("Slack 签名校验失败")
		}
		return nil
	}
	provided := request.Header.Get("X-Niuma-Integration-Token")
	if provided == "" {
		provided = request.URL.Query().Get("token")
	}
	if settings.WebhookSecret == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(settings.WebhookSecret)) != 1 {
		return errf("Webhook Token 校验失败")
	}
	return nil
}

func integrationEventKey(kind string, body []byte) string {
	var payload map[string]any
	if json.Unmarshal(body, &payload) == nil {
		for _, key := range []string{"event_id", "id"} {
			if value := strings.TrimSpace(fmt.Sprint(payload[key])); value != "" && value != "<nil>" {
				return kind + ":" + value
			}
		}
		if kind == "zentao" {
			parts := []string{fmt.Sprint(payload["objectType"]), fmt.Sprint(payload["objectID"]), fmt.Sprint(payload["action"]), fmt.Sprint(payload["date"])}
			joined := strings.Join(parts, ":")
			if !strings.Contains(joined, "<nil>") {
				return kind + ":" + joined
			}
		}
		if kind == "jira" {
			if issue, ok := payload["issue"].(map[string]any); ok {
				parts := []string{fmt.Sprint(payload["webhookEvent"]), fmt.Sprint(issue["id"]), fmt.Sprint(payload["timestamp"])}
				joined := strings.Join(parts, ":")
				if !strings.Contains(joined, "<nil>") {
					return kind + ":" + joined
				}
			}
		}
	}
	sum := sha256.Sum256(body)
	return kind + ":sha256:" + hex.EncodeToString(sum[:])
}

func integrationEventSummary(body []byte) string {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return "收到第三方事件"
	}
	for _, key := range []string{"webhookEvent", "action", "type"} {
		if value := strings.TrimSpace(fmt.Sprint(payload[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	return "收到第三方事件"
}

func slackChallenge(body []byte) (string, bool) {
	var payload struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
	}
	if json.Unmarshal(bytes.TrimSpace(body), &payload) != nil || payload.Type != "url_verification" || payload.Challenge == "" {
		return "", false
	}
	return payload.Challenge, true
}
