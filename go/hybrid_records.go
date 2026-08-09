package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type integrationSettings struct {
	Enabled    bool   `json:"enabled"`
	AppID      string `json:"app_id"`
	AppSecret  string `json:"app_secret"`
	BaseToken  string `json:"base_token"`
	TableID    string `json:"table_id"`
	SyncPeriod int    `json:"sync_period"`
}

type integrationStatus struct {
	Enabled       bool   `json:"enabled"`
	Configured    bool   `json:"configured"`
	AppID         string `json:"app_id"`
	HasAppSecret  bool   `json:"has_app_secret"`
	BaseToken     string `json:"base_token"`
	TableID       string `json:"table_id"`
	SyncPeriod    int    `json:"sync_period"`
	Syncing       bool   `json:"syncing"`
	LastSyncAt    string `json:"last_sync_at"`
	LastSyncError string `json:"last_sync_error"`
}

type hybridRecords struct {
	local        *localRecords
	settingsPath string
	mu           sync.RWMutex
	settings     integrationSettings
	feishu       *Feishu
	syncMu       sync.Mutex
	syncing      bool
	lastSyncAt   time.Time
	lastSyncErr  string
}

func defaultIntegrationSettings(c *Config) integrationSettings {
	return integrationSettings{
		Enabled: c.FeishuEnabled, AppID: c.AppID, AppSecret: c.AppSecret,
		BaseToken: c.BaseToken, TableID: c.TableID, SyncPeriod: 60,
	}
}

func loadIntegrationSettings(c *Config) integrationSettings {
	settings := defaultIntegrationSettings(c)
	path := filepath.Join(c.StateDir, "integration.json")
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &settings)
	}
	if settings.SyncPeriod < 15 {
		settings.SyncPeriod = 60
	}
	return settings
}

func newHybridRecords(local *localRecords, stateDir string, settings integrationSettings) *hybridRecords {
	h := &hybridRecords{local: local, settingsPath: filepath.Join(stateDir, "integration.json")}
	h.configure(settings)
	return h
}

func (h *hybridRecords) configure(settings integrationSettings) {
	if settings.SyncPeriod < 15 {
		settings.SyncPeriod = 60
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.settings = settings
	if settings.Enabled && integrationConfigured(settings) {
		copyCfg := *cfg
		copyCfg.AppID, copyCfg.AppSecret = settings.AppID, settings.AppSecret
		copyCfg.BaseToken, copyCfg.TableID = settings.BaseToken, settings.TableID
		h.feishu = newFeishu(&copyCfg)
	} else {
		h.feishu = nil
	}
}

func integrationConfigured(settings integrationSettings) bool {
	return strings.TrimSpace(settings.AppID) != "" && strings.TrimSpace(settings.AppSecret) != "" &&
		strings.TrimSpace(settings.BaseToken) != "" && strings.TrimSpace(settings.TableID) != ""
}

func (h *hybridRecords) saveSettings(settings integrationSettings) error {
	if settings.SyncPeriod < 15 {
		settings.SyncPeriod = 60
	}
	if settings.Enabled && !integrationConfigured(settings) {
		return errf("启用飞书需要填写 App ID、App Secret、Base Token 和 Table ID")
	}
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(h.settingsPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(h.settingsPath, append(b, '\n'), 0o600); err != nil {
		return err
	}
	h.configure(settings)
	return nil
}

func (h *hybridRecords) currentSettings() integrationSettings {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.settings
}

func (h *hybridRecords) currentFeishu() (*Feishu, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.feishu, h.settings.Enabled && h.feishu != nil
}

func (h *hybridRecords) status() integrationStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return integrationStatus{
		Enabled: h.settings.Enabled, Configured: integrationConfigured(h.settings),
		AppID: maskConfig(h.settings.AppID), HasAppSecret: h.settings.AppSecret != "",
		BaseToken: maskConfig(h.settings.BaseToken), TableID: h.settings.TableID,
		SyncPeriod: h.settings.SyncPeriod, Syncing: h.syncing,
		LastSyncAt: formatOptionalTime(h.lastSyncAt), LastSyncError: h.lastSyncErr,
	}
}

func maskConfig(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 6 {
		if value == "" {
			return ""
		}
		return "••••••"
	}
	return value[:3] + "••••" + value[len(value)-3:]
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

func (h *hybridRecords) listRecords() ([]Record, error) { return h.local.listRecords() }

func (h *hybridRecords) createRecord(fields map[string]any) (*Record, error) {
	_, enabled := h.currentFeishu()
	state := "local"
	if enabled {
		state = "pending"
	}
	rec, err := h.local.createRecord(fields, state)
	if err != nil {
		return nil, err
	}
	if enabled {
		h.pushRecord(*rec)
	}
	return rec, nil
}

func (h *hybridRecords) updateRecord(recordID string, fields map[string]any) error {
	_, enabled := h.currentFeishu()
	if err := h.local.updateRecord(recordID, fields, enabled); err != nil {
		return err
	}
	if enabled {
		if rec, err := h.local.getRecord(recordID); err == nil {
			h.pushRecord(*rec)
		}
	}
	return nil
}

func (h *hybridRecords) pushRecord(rec Record) {
	f, enabled := h.currentFeishu()
	if !enabled {
		return
	}
	meta, err := h.local.meta(rec.RecordID)
	if err != nil {
		return
	}
	if meta.ExternalID == "" {
		created, err := f.createRecord(rec.Fields)
		if err != nil {
			h.local.markSyncError(rec.RecordID, err)
			return
		}
		_ = h.local.markSynced(rec.RecordID, created.RecordID, int64(created.LastModifiedTime))
		return
	}
	if err := f.updateRecord(meta.ExternalID, rec.Fields); err != nil {
		h.local.markSyncError(rec.RecordID, err)
		return
	}
	_ = h.local.markSynced(rec.RecordID, meta.ExternalID, time.Now().UnixMilli())
}

func (h *hybridRecords) syncNow() error {
	if !h.syncMu.TryLock() {
		return errf("同步正在进行中")
	}
	defer h.syncMu.Unlock()
	h.mu.Lock()
	h.syncing = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.syncing = false
		h.mu.Unlock()
	}()
	f, enabled := h.currentFeishu()
	if !enabled {
		return errf("飞书集成未启用或配置不完整")
	}
	if pending, err := h.local.pendingRecords(); err == nil {
		for _, rec := range pending {
			h.pushRecord(rec)
		}
	}
	remote, err := f.listRecords()
	if err != nil {
		h.recordSyncResult(err)
		return err
	}
	for _, rec := range remote {
		meta, lookupErr := h.local.metaByExternal(rec.RecordID)
		switch {
		case lookupErr == sql.ErrNoRows:
			if err := h.local.importRemote(rec); err != nil {
				h.recordSyncResult(err)
				return err
			}
		case lookupErr != nil:
			h.recordSyncResult(lookupErr)
			return lookupErr
		case meta.Archived || meta.SyncState != "synced":
			continue
		case int64(rec.LastModifiedTime) > meta.RemoteModifiedAt:
			if err := h.local.importRemoteForLocal(meta.RecordID, rec); err != nil {
				h.recordSyncResult(err)
				return err
			}
		}
	}
	h.recordSyncResult(nil)
	return nil
}

func (h *hybridRecords) recordSyncResult(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		h.lastSyncErr = err.Error()
		return
	}
	h.lastSyncAt = time.Now()
	h.lastSyncErr = ""
}

func (h *hybridRecords) testConnection(settings integrationSettings) error {
	if !integrationConfigured(settings) {
		return errf("请先填写完整飞书配置")
	}
	copyCfg := *cfg
	copyCfg.AppID, copyCfg.AppSecret = settings.AppID, settings.AppSecret
	copyCfg.BaseToken, copyCfg.TableID = settings.BaseToken, settings.TableID
	_, err := newFeishu(&copyCfg).listRecords()
	return err
}

func (h *hybridRecords) archiveRecord(id string) error                { return h.local.archive(id) }
func (h *hybridRecords) recordMeta(id string) (recordSyncMeta, error) { return h.local.meta(id) }

func (h *hybridRecords) sendText(chatID, text string) error {
	if f, ok := h.currentFeishu(); ok {
		return f.sendText(chatID, text)
	}
	return nil
}
func (h *hybridRecords) sendCard(chatID string, card map[string]any) (string, error) {
	if f, ok := h.currentFeishu(); ok {
		return f.sendCard(chatID, card)
	}
	return "", nil
}
func (h *hybridRecords) patchCard(messageID string, card map[string]any) error {
	if f, ok := h.currentFeishu(); ok {
		return f.patchCard(messageID, card)
	}
	return nil
}
func (h *hybridRecords) notify(chatID, text string) {
	if err := h.sendText(chatID, text); err != nil {
		elog("发消息失败 chat=%s: %v", chatID, err)
	}
}
func (h *hybridRecords) notifyCard(chatID string, card map[string]any) {
	if _, err := h.sendCard(chatID, card); err != nil {
		elog("发卡片失败 chat=%s: %v", chatID, err)
	}
}
