package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Feishu 是飞书 OpenAPI 数据层：tenant_access_token 自动刷新 + 多维表格记录 + IM 消息。
type Feishu struct {
	baseURL   string
	appID     string
	appSecret string
	baseToken string
	tableID   string
	http      *http.Client
	mu        sync.Mutex
	token     string
	tokenExp  time.Time
}

type Record struct {
	RecordID         string         `json:"record_id"`
	Fields           map[string]any `json:"fields"`
	LastModifiedTime flexibleInt64  `json:"last_modified_time,omitempty"`
}

type flexibleInt64 int64

func (v *flexibleInt64) UnmarshalJSON(data []byte) error {
	raw := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if raw == "" || raw == "null" {
		*v = 0
		return nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return err
	}
	*v = flexibleInt64(parsed)
	return nil
}

type recordBackend interface {
	listRecords() ([]Record, error)
	createRecord(fields map[string]any) (*Record, error)
	updateRecord(recordID string, fields map[string]any) error
	sendText(chatID, text string) error
	sendCard(chatID string, card map[string]any) (string, error)
	patchCard(messageID string, card map[string]any) error
	notify(chatID, text string)
	notifyCard(chatID string, card map[string]any)
}

func newFeishu(c *Config) *Feishu {
	base := envText("FEISHU_BASE_URL", "https://open.feishu.cn")
	return &Feishu{
		baseURL:   base,
		appID:     c.AppID,
		appSecret: c.AppSecret,
		baseToken: c.BaseToken,
		tableID:   c.TableID,
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

func (f *Feishu) tenantToken() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.token != "" && time.Now().Before(f.tokenExp) {
		return f.token, nil
	}
	body, _ := json.Marshal(map[string]string{"app_id": f.appID, "app_secret": f.appSecret})
	req, _ := http.NewRequest("POST", f.baseURL+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := f.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var d struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return "", err
	}
	if d.Code != 0 {
		return "", errf("取 token 失败: code=%d msg=%s", d.Code, d.Msg)
	}
	f.token = d.TenantAccessToken
	f.tokenExp = time.Now().Add(time.Duration(d.Expire-300) * time.Second)
	return f.token, nil
}

// api 发一个带鉴权的请求，返回 data 字段的原始 JSON。
func (f *Feishu) api(method, path string, body any) (json.RawMessage, error) {
	tok, err := f.tenantToken()
	if err != nil {
		return nil, err
	}
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, f.baseURL+path, rdr)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := f.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, errf("解析响应失败 %s %s: %v", method, path, err)
	}
	if env.Code != 0 {
		return nil, errf("feishu api %s %s 失败: code=%d msg=%s", method, path, env.Code, env.Msg)
	}
	return env.Data, nil
}

func (f *Feishu) recBase() string {
	return "/open-apis/bitable/v1/apps/" + f.baseToken + "/tables/" + f.tableID + "/records"
}

func (f *Feishu) listRecords() ([]Record, error) {
	var records []Record
	pageToken := ""
	for {
		q := url.Values{"page_size": {"500"}}
		if pageToken != "" {
			q.Set("page_token", pageToken)
		}
		data, err := f.api("GET", f.recBase()+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var d struct {
			Items     []Record `json:"items"`
			HasMore   bool     `json:"has_more"`
			PageToken string   `json:"page_token"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}
		records = append(records, d.Items...)
		if !d.HasMore || d.PageToken == "" {
			return records, nil
		}
		pageToken = d.PageToken
	}
}

func (f *Feishu) updateRecord(recordID string, fields map[string]any) error {
	_, err := f.api("PUT", f.recBase()+"/"+recordID, map[string]any{"fields": fields})
	return err
}

func (f *Feishu) createRecord(fields map[string]any) (*Record, error) {
	data, err := f.api("POST", f.recBase(), map[string]any{"fields": fields})
	if err != nil {
		return nil, err
	}
	var d struct {
		Record Record `json:"record"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	if d.Record.RecordID == "" {
		return nil, errf("创建记录返回缺少 record_id")
	}
	return &d.Record, nil
}

func (f *Feishu) sendText(chatID, text string) error {
	if chatID == "" {
		return nil
	}
	content, _ := json.Marshal(map[string]string{"text": text})
	q := url.Values{"receive_id_type": {"chat_id"}}
	_, err := f.api("POST", "/open-apis/im/v1/messages?"+q.Encode(), map[string]any{
		"receive_id": chatID, "msg_type": "text", "content": string(content),
	})
	return err
}

// sendCard 发交互卡片，返回 message_id（用于后续原地更新）。
func (f *Feishu) sendCard(chatID string, card map[string]any) (string, error) {
	if chatID == "" {
		return "", nil
	}
	content, _ := json.Marshal(card)
	q := url.Values{"receive_id_type": {"chat_id"}}
	data, err := f.api("POST", "/open-apis/im/v1/messages?"+q.Encode(), map[string]any{
		"receive_id": chatID, "msg_type": "interactive", "content": string(content),
	})
	if err != nil {
		return "", err
	}
	var d struct {
		MessageID string `json:"message_id"`
	}
	json.Unmarshal(data, &d)
	return d.MessageID, nil
}

func (f *Feishu) patchCard(messageID string, card map[string]any) error {
	content, _ := json.Marshal(card)
	_, err := f.api("PATCH", "/open-apis/im/v1/messages/"+messageID, map[string]any{"content": string(content)})
	return err
}

// 安全发送：失败只记日志不阻断主流程。
func (f *Feishu) notify(chatID, text string) {
	if err := f.sendText(chatID, text); err != nil {
		elog("发消息失败 chat=%s: %v", chatID, err)
	}
}

func (f *Feishu) notifyCard(chatID string, card map[string]any) {
	if _, err := f.sendCard(chatID, card); err != nil {
		elog("发卡片失败 chat=%s: %v", chatID, err)
	}
}
