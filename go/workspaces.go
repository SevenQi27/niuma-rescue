package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

type Workspace struct {
	Key          string
	Path         string
	SCM          string // git | svn
	WorkMode     string // worktree | inline
	WorktreeBase string // per-workspace worktree root; empty uses global default
	RulesSource  string // source checkout whose AGENTS.md/.claude/rules are linked into worktrees
	BaseRef      string
	TargetBranch string
	TestCmd      string
	PRProvider   string // none | github | gitlab
	GHRepo       string
	PushEnabled  bool
	PREnabled    bool
}

type wsFile struct {
	Default string            `json:"default"`
	Items   map[string]wsItem `json:"items"`
}
type wsItem struct {
	Path         string `json:"path"`
	SCM          string `json:"scm"`
	WorkMode     string `json:"work_mode"`
	WorktreeBase string `json:"worktree_base"`
	RulesSource  string `json:"rules_source"`
	Base         string `json:"base"`
	TargetBranch string `json:"target_branch"`
	TestCmd      string `json:"test_cmd"`
	PRProvider   string `json:"pr_provider"`
	GHRepo       string `json:"gh_repo"`
	PushEnabled  bool   `json:"push_enabled"`
	PREnabled    bool   `json:"pr_enabled"`
}

var (
	wsCache *wsFile
	wsMu    sync.RWMutex
)

// loadWorkspaces 通过读写锁完成并发安全的懒加载；管理页保存后可直接替换缓存，
// 不需要重启服务，同时避免并发首调时读到半填充的配置。
func loadWorkspaces() *wsFile {
	wsMu.RLock()
	if wsCache != nil {
		defer wsMu.RUnlock()
		return wsCache
	}
	wsMu.RUnlock()
	wsMu.Lock()
	defer wsMu.Unlock()
	if wsCache == nil {
		c := &wsFile{Items: map[string]wsItem{}}
		if b, err := os.ReadFile(cfg.WorkspacesFile); err == nil {
			_ = json.Unmarshal(b, c)
		}
		if c.Items == nil {
			c.Items = map[string]wsItem{}
		}
		wsCache = c
	}
	return wsCache
}

func workspaceConfigSnapshot() wsFile {
	f := loadWorkspaces()
	out := wsFile{Default: f.Default, Items: make(map[string]wsItem, len(f.Items))}
	for key, item := range f.Items {
		out.Items[key] = item
	}
	return out
}

func saveWorkspaceConfig(next wsFile) error {
	if len(next.Items) == 0 {
		return errf("至少保留一个工作区")
	}
	if _, ok := next.Items[next.Default]; !ok {
		return errf("默认工作区 `%s` 不存在", next.Default)
	}
	keyRe := regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	for key, item := range next.Items {
		if !keyRe.MatchString(key) {
			return errf("工作区 key `%s` 只能包含字母、数字、点、下划线和短横线", key)
		}
		if !filepath.IsAbs(item.Path) {
			return errf("工作区 `%s` 的 path 必须是绝对路径", key)
		}
		if _, err := os.Stat(item.Path); err != nil {
			return errf("工作区 `%s` 路径不可用: %v", key, err)
		}
		if item.SCM != "" && item.SCM != "git" && item.SCM != "svn" {
			return errf("工作区 `%s` 的 scm 只能是 git 或 svn", key)
		}
		if item.WorkMode != "" && item.WorkMode != "inline" && item.WorkMode != "worktree" {
			return errf("工作区 `%s` 的 work_mode 只能是 inline 或 worktree", key)
		}
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.WorkspacesFile), 0o755); err != nil {
		return err
	}
	tmp := cfg.WorkspacesFile + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, cfg.WorkspacesFile); err != nil {
		return err
	}
	wsMu.Lock()
	wsCache = &next
	wsMu.Unlock()
	return nil
}

// defaultWorkspace 从 .env 合成（没有 workspaces.json 时）。
func defaultWorkspace() Workspace {
	return Workspace{
		Key: "default", Path: cfg.RepoPath, SCM: "git", WorkMode: "inline", BaseRef: cfg.BaseRef,
		TargetBranch: lastSeg(cfg.BaseRef), TestCmd: cfg.TestCmd, PRProvider: "none",
		GHRepo: cfg.GHRepo, PushEnabled: cfg.PushEnabled, PREnabled: cfg.PREnabled,
	}
}

func workspaceGet(key string) (Workspace, error) {
	f := loadWorkspaces()
	if len(f.Items) == 0 {
		if key == "" || key == "default" {
			return defaultWorkspace(), nil
		}
		return Workspace{}, errf("未知工作区 `%s`：未配置 workspaces.json", key)
	}
	if key == "" {
		key = f.Default
	}
	it, ok := f.Items[key]
	if !ok {
		var keys []string
		for k := range f.Items {
			keys = append(keys, k)
		}
		return Workspace{}, errf("未知工作区 `%s`，可选：%s", key, strings.Join(keys, ", "))
	}
	scm := it.SCM
	if scm == "" {
		scm = "git"
	}
	mode := it.WorkMode
	if mode == "" {
		mode = "inline"
	}
	base := it.Base
	if base == "" {
		base = "origin/main"
	}
	tb := it.TargetBranch
	if tb == "" {
		tb = lastSeg(base)
	}
	prov := it.PRProvider
	if prov == "" {
		prov = "none"
	}
	return Workspace{
		Key: key, Path: it.Path, SCM: scm, WorkMode: mode, BaseRef: base, TargetBranch: tb,
		WorktreeBase: it.WorktreeBase, RulesSource: it.RulesSource, TestCmd: it.TestCmd, PRProvider: prov, GHRepo: it.GHRepo,
		PushEnabled: it.PushEnabled, PREnabled: it.PREnabled,
	}, nil
}

func workspaceKeys() []string {
	f := loadWorkspaces()
	if len(f.Items) == 0 {
		return []string{"default"}
	}
	var keys []string
	for k := range f.Items {
		keys = append(keys, k)
	}
	return keys
}

func (w Workspace) safeKey() string {
	return regexp.MustCompile(`[^A-Za-z0-9_.-]`).ReplaceAllString(w.Key, "_")
}

var wsTokenRe = regexp.MustCompile(`#([A-Za-z0-9_.-]+)`)

// parseWorkspaceToken 从文本里抽 #key（返回去掉 token 的文本 + key）。
func parseWorkspaceToken(body string) (string, string) {
	m := wsTokenRe.FindStringSubmatch(body)
	if m == nil {
		return body, ""
	}
	cleaned := strings.TrimSpace(wsTokenRe.ReplaceAllString(body, ""))
	return cleaned, m[1]
}

func lastSeg(ref string) string {
	if i := strings.LastIndexByte(ref, '/'); i >= 0 {
		return ref[i+1:]
	}
	return ref
}
