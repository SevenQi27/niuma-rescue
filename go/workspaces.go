package main

import (
	"encoding/json"
	"os"
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
	wsOnce  sync.Once
)

// loadWorkspaces 只完整初始化一次（sync.Once）：避免并发首调时读到半填充的缓存，
// 否则不同 goroutine 解析出的工作区/锁 key 会不一致，按工作区的串行锁形同虚设。
func loadWorkspaces() *wsFile {
	wsOnce.Do(func() {
		c := &wsFile{Items: map[string]wsItem{}}
		if b, err := os.ReadFile(cfg.WorkspacesFile); err == nil {
			json.Unmarshal(b, c)
		}
		wsCache = c
	})
	return wsCache
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
