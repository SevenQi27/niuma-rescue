package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type pubResult struct {
	OK     bool
	Note   string
	Link   string
	Detail string
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (w Workspace) inline() bool {
	return strings.EqualFold(strings.TrimSpace(w.WorkMode), "inline")
}

// scmPrepare 为需求准备（或复用）开发目录。inline 模式直接使用原仓库。
func scmPrepare(ws Workspace, reqID string) (workPath, branch string, err error) {
	return scmPrepareTask(ws, "REQ", reqID, false)
}

// scmPrepareBug 无论工作区默认策略是什么，都为 Bug 建立隔离 worktree。
func scmPrepareBug(ws Workspace, bugID string) (Workspace, string, string, error) {
	isolated := ws
	isolated.WorkMode = "worktree"
	workPath, branch, err := scmPrepareTask(isolated, "BUG", bugID, true)
	return isolated, workPath, branch, err
}

func bugBranchName(bugID string) string {
	return "niuma/BUG-" + bugID
}

// verifyBugMerged 只做本地 Git 证据校验：目标分支必须包含 Bug 提交，
// 或包含与 Bug 分支等价的补丁（兼容 squash merge）。
func verifyBugMerged(ws Workspace, bugID string) (bool, string) {
	branch := bugBranchName(bugID)
	if out, err := git(ws.Path, "rev-parse", "--verify", branch+"^{commit}"); err != nil {
		return false, "找不到 Bug 分支 " + branch + "：" + strings.TrimSpace(out)
	}
	targets := []string{ws.TargetBranch}
	if ws.BaseRef != "" && ws.BaseRef != ws.TargetBranch {
		targets = append(targets, ws.BaseRef)
	}
	for _, target := range targets {
		if strings.TrimSpace(target) == "" {
			continue
		}
		if _, err := git(ws.Path, "rev-parse", "--verify", target+"^{commit}"); err != nil {
			continue
		}
		if _, err := git(ws.Path, "merge-base", "--is-ancestor", branch, target); err == nil {
			return true, target + " 已包含 " + branch
		}
		out, err := git(ws.Path, "cherry", target, branch)
		if err != nil {
			continue
		}
		lines := strings.Fields(strings.TrimSpace(out))
		if len(lines) > 0 {
			equivalent := true
			for i := 0; i < len(lines); i += 2 {
				if lines[i] != "-" {
					equivalent = false
					break
				}
			}
			if equivalent {
				return true, target + " 已包含与 " + branch + " 等价的补丁"
			}
		}
	}
	target := ws.TargetBranch
	if target == "" {
		target = ws.BaseRef
	}
	return false, "目标分支 " + target + " 尚未包含 " + branch
}

func scmPrepareTask(ws Workspace, kind, taskID string, forceWorktree bool) (workPath, branch string, err error) {
	if ws.inline() && !forceWorktree {
		out, _ := git(ws.Path, "branch", "--show-current")
		branch = strings.TrimSpace(out)
		if branch == "" {
			branch = ws.TargetBranch
		}
		return ws.Path, branch, nil
	}
	base := strings.TrimSpace(ws.WorktreeBase)
	if base == "" {
		base = filepath.Join(cfg.WorktreeBase, ws.safeKey())
	}
	if e := os.MkdirAll(base, 0o755); e != nil {
		return "", "", e
	}
	workPath = filepath.Join(base, kind+"-"+taskID)
	branch = "niuma/" + kind + "-" + taskID
	if _, e := os.Stat(workPath); e == nil {
		out, branchErr := git(workPath, "branch", "--show-current")
		if branchErr != nil || strings.TrimSpace(out) != branch {
			return "", "", errf("worktree 目录已存在但分支不匹配: want=%s got=%s", branch, strings.TrimSpace(out))
		}
		if e := ensureBranchNoUpstream(ws.Path, branch); e != nil {
			return "", "", e
		}
		if e := attachWorkspaceRules(workPath, ws.RulesSource); e != nil {
			return "", "", e
		}
		return workPath, branch, nil // 复用同一任务的现有 worktree
	}
	if strings.HasPrefix(ws.BaseRef, "origin/") {
		if out, e := git(ws.Path, "fetch", "--quiet", "origin"); e != nil {
			return "", "", errf("更新 %s 失败: %s", ws.BaseRef, strings.TrimSpace(out))
		}
	}
	_, branchErr := git(ws.Path, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	var out string
	var e error
	if branchErr == nil {
		out, e = git(ws.Path, "worktree", "add", workPath, branch)
	} else {
		out, e = git(ws.Path, "worktree", "add", "--no-track", "-b", branch, workPath, ws.BaseRef)
	}
	if e != nil {
		return "", "", errf("worktree 创建失败: %s", strings.TrimSpace(out))
	}
	if e := ensureBranchNoUpstream(ws.Path, branch); e != nil {
		return "", "", e
	}
	if e := attachWorkspaceRules(workPath, ws.RulesSource); e != nil {
		return "", "", e
	}
	return workPath, branch, nil
}

func attachWorkspaceRules(workPath, source string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil
	}
	links := []struct{ source, target string }{
		{filepath.Join(source, "AGENTS.md"), filepath.Join(workPath, "AGENTS.md")},
		{filepath.Join(source, ".claude", "rules"), filepath.Join(workPath, ".claude", "rules")},
	}
	if e := os.MkdirAll(filepath.Join(workPath, ".claude"), 0o755); e != nil {
		return errf("创建规则目录失败: %v", e)
	}
	for _, link := range links {
		if _, e := os.Stat(link.source); e != nil {
			return errf("规则来源不存在: %s", link.source)
		}
		if current, e := os.Readlink(link.target); e == nil {
			if current == link.source {
				continue
			}
			return errf("规则链接已指向其他位置: %s -> %s", link.target, current)
		} else if !os.IsNotExist(e) {
			return errf("规则目标已存在，拒绝覆盖: %s", link.target)
		}
		if e := os.Symlink(link.source, link.target); e != nil {
			return errf("创建规则链接失败: %s: %v", link.target, e)
		}
	}
	return nil
}

func ensureBranchNoUpstream(repo, branch string) error {
	ref := "refs/heads/" + branch
	out, e := git(repo, "for-each-ref", "--format=%(upstream:short)", ref)
	if e != nil {
		return errf("检查分支 upstream 失败: %s", strings.TrimSpace(out))
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	if out, e = git(repo, "branch", "--unset-upstream", branch); e != nil {
		return errf("清除分支 upstream 失败: %s", strings.TrimSpace(out))
	}
	out, e = git(repo, "for-each-ref", "--format=%(upstream:short)", ref)
	if e != nil || strings.TrimSpace(out) != "" {
		return errf("分支 %s 仍在跟踪 %s", branch, strings.TrimSpace(out))
	}
	return nil
}

func changedFiles(ws Workspace, wt string) []string {
	if ws.inline() {
		out, _ := git(wt, "status", "--porcelain")
		seen := map[string]bool{}
		var files []string
		for _, l := range strings.Split(out, "\n") {
			if len(l) < 4 {
				continue
			}
			p := strings.TrimSpace(l[3:])
			if strings.Contains(p, " -> ") {
				parts := strings.Split(p, " -> ")
				p = strings.TrimSpace(parts[len(parts)-1])
			}
			if p != "" && !seen[p] {
				seen[p] = true
				files = append(files, p)
			}
		}
		return files
	}
	out, _ := git(wt, "diff", "--name-only", ws.BaseRef)
	untracked, _ := git(wt, "ls-files", "--others", "--exclude-standard")
	out += "\n" + untracked
	seen := map[string]bool{}
	var files []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" && !seen[l] {
			seen[l] = true
			files = append(files, l)
		}
	}
	return files
}

func productChangedFiles(files []string) []string {
	var out []string
	for _, f := range files {
		if strings.HasPrefix(f, dossierDir+"/") {
			continue
		}
		out = append(out, f)
	}
	return out
}

func diffText(ws Workspace, wt string) string {
	if ws.inline() {
		out, _ := git(wt, "diff", "--stat")
		body, _ := git(wt, "diff")
		untracked, _ := git(wt, "ls-files", "--others", "--exclude-standard")
		if strings.TrimSpace(untracked) != "" {
			body += "\n\nUntracked files:\n" + untracked
		}
		return strings.TrimSpace(out + "\n" + body)
	}
	out, _ := git(wt, "diff", ws.BaseRef)
	untracked, _ := git(wt, "ls-files", "--others", "--exclude-standard")
	if strings.TrimSpace(untracked) != "" {
		out += "\n\nUntracked files:\n" + untracked
	}
	return out
}

func gitCommitAll(wt, msg string) {
	git(wt, "add", "-A")
	git(wt, "commit", "-m", msg) // 没改动则失败，忽略
}

func gitCommitAllChecked(wt, msg string) error {
	if out, err := git(wt, "add", "-A", "--", ".", ":(exclude).pipeline", ":(exclude).pipeline/**"); err != nil {
		return errf("git add: %s", strings.TrimSpace(out))
	}
	if out, err := git(wt, "commit", "-m", msg); err != nil {
		return errf("git commit: %s", strings.TrimSpace(out))
	}
	return nil
}

// afterDevelop：开发完成后发布（push 开则推分支）。
func afterDevelop(ws Workspace, wt, branch string) pubResult {
	if ws.inline() {
		return pubResult{OK: true, Note: "inline 模式：已保留在当前工作区，未提交/未推送", Link: branch}
	}
	if !ws.PushEnabled {
		return pubResult{OK: true, Note: "未开 push（本地分支 " + branch + "）", Link: branch}
	}
	out, err := git(wt, "push", "-u", "origin", branch, "--force-with-lease")
	if err != nil {
		return pubResult{OK: false, Detail: strings.TrimSpace(out)}
	}
	return pubResult{OK: true, Note: "已推送 " + branch, Link: branch}
}

// afterReview：review 通过后建 PR（github via gh）或仅 push。
func afterReview(ws Workspace, wt, branch, title, body string) pubResult {
	if ws.inline() {
		return pubResult{OK: true, Note: "inline 模式：待人工决定提交/合并", Link: branch}
	}
	if ws.PushEnabled {
		if out, err := git(wt, "push", "-u", "origin", branch, "--force-with-lease"); err != nil {
			return pubResult{OK: false, Detail: strings.TrimSpace(out)}
		}
	}
	if ws.PREnabled && ws.PRProvider == "github" {
		args := []string{"pr", "create", "--title", title, "--body", body, "--head", branch, "--base", ws.TargetBranch}
		if ws.GHRepo != "" {
			args = append(args, "--repo", ws.GHRepo)
		}
		cmd := exec.Command("gh", args...)
		cmd.Dir = wt
		out, err := cmd.CombinedOutput()
		if err != nil {
			return pubResult{OK: false, Detail: strings.TrimSpace(string(out))}
		}
		link := strings.TrimSpace(lastNonEmpty(string(out)))
		return pubResult{OK: true, Note: "已建 PR", Link: link}
	}
	if ws.PushEnabled {
		return pubResult{OK: true, Note: "已推送 " + branch, Link: branch}
	}
	return pubResult{OK: true, Note: "未开 push/PR（本地分支 " + branch + "）", Link: branch}
}

func lastNonEmpty(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}
