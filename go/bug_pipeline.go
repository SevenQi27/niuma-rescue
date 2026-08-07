package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type bugGraphInput struct {
	RecordID       string `json:"record_id"`
	ThreadID       string `json:"thread_id"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	Clarifications string `json:"clarifications"`
	Worktree       string `json:"worktree"`
	BaseRef        string `json:"base_ref"`
	TestCmd        string `json:"test_cmd"`
	FixAgent       string `json:"fix_agent"`
	ReviewAgent    string `json:"review_agent"`
	AgentRunner    string `json:"agent_runner"`
	FixTimeout     int    `json:"fix_timeout"`
	ReviewTimeout  int    `json:"review_timeout"`
	TestTimeout    int    `json:"test_timeout"`
	MaxRepairs     int    `json:"max_repairs"`
	CheckpointDB   string `json:"checkpoint_db"`
	ProgressFile   string `json:"progress_file"`
}

type bugGraphOutput struct {
	Status       string `json:"status"`
	Summary      string `json:"summary"`
	Diagnosis    string `json:"diagnosis"`
	Questions    string `json:"questions"`
	TestOK       bool   `json:"test_ok"`
	TestOutput   string `json:"test_output"`
	ReviewOutput string `json:"review_output"`
	Iteration    int    `json:"iteration"`
}

func runBugGraph(input bugGraphInput, runID string, st *Store) (bugGraphOutput, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return bugGraphOutput{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutBug)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.BugGraphPython, "-m", "buggraph")
	cmd.Dir = cfg.BugGraphDir
	env := replaceEnv(scrubbedEnv(), "PYTHONPATH", filepath.Join(cfg.BugGraphDir, "src"))
	cmd.Env = replaceEnv(env, "LANGGRAPH_STRICT_MSGPACK", "true")
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return bugGraphOutput{}, errf("启动 LangGraph 失败: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				return bugGraphOutput{}, errf("LangGraph 失败: %s", trunc(strings.TrimSpace(stderr.String()), 1200))
			}
			var output bugGraphOutput
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
				return bugGraphOutput{}, errf("LangGraph 返回无效 JSON: %v; stdout=%s; stderr=%s", err,
					trunc(stdout.String(), 500), trunc(stderr.String(), 500))
			}
			return output, nil
		case <-ticker.C:
			if st != nil && runID != "" {
				st.heartbeat(runID)
			}
		case <-ctx.Done():
			return bugGraphOutput{}, errf("LangGraph 总超时（%ds）", cfg.TimeoutBug)
		}
	}
}

func replaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return append(out, prefix+value)
}

func (a *App) writeBugDossier(wt, bugID string, rec *Record) string {
	dir := filepath.Join(wt, dossierDir, "BUG-"+bugID)
	_ = os.MkdirAll(dir, 0o755)
	body := "# Bug\n\n" + recTitle(rec) + "\n\n## 描述\n\n" + fieldText(rec.Fields[FDesc])
	if clarification := strings.TrimSpace(fieldText(rec.Fields[FClarify])); clarification != "" {
		body += "\n\n## 人工补充\n\n" + clarification
	}
	attachments := storedWebImages(fieldText(rec.Fields[FLog]))
	if len(attachments) > 0 {
		attachmentDir := filepath.Join(dir, "attachments")
		_ = os.MkdirAll(attachmentDir, 0o755)
		body += "\n\n## 附件\n"
		for _, attachment := range attachments {
			source := filepath.Join(cfg.StateDir, "bug-images", bugID, attachment.File)
			target := filepath.Join(attachmentDir, attachment.File)
			if data, err := os.ReadFile(source); err == nil {
				_ = os.WriteFile(target, data, 0o644)
				body += "\n- `attachments/" + attachment.File + "`（" + attachment.Name + "，" + attachment.MIME + "）"
			}
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "bug.md"), []byte(body+"\n"), 0o644)
	return dir
}

func (a *App) blockBug(rec *Record, reason string) {
	appendBugProgress(cfg.StateDir, rec.RecordID, "pipeline", "failed", "Niuma", reason, 0)
	fails := fieldInt(rec.Fields[FFails]) + 1
	_ = a.advance(rec, SBlocked, "[bug] "+trunc(reason, 1200)+" → 已阻塞", map[string]any{FFails: fails})
	chat := fieldText(rec.Fields[FChat])
	a.fs.notifyCard(chat, blockedCard(rec, reason))
	a.fs.notify(chat, "⚠️ Bug「"+recTitle(rec)+"」已阻塞，需要人工介入："+trunc(reason, 300))
}

func (a *App) handleBug(rec *Record, runID string) {
	rid := rec.RecordID
	chat := fieldText(rec.Fields[FChat])
	ws := a.workspaceFor(rec)
	resetBugProgress(cfg.StateDir, rid)
	appendBugProgress(cfg.StateDir, rid, "prepare", "running", "Niuma", "正在从目标基线准备独立修复目录", 0)
	if !strings.EqualFold(ws.SCM, "git") {
		a.blockBug(rec, "Bug 最小链路当前只支持 git workspace")
		return
	}
	a.gitMu.Lock()
	isolated, wt, branch, err := scmPrepareBug(ws, rid)
	a.gitMu.Unlock()
	if err != nil {
		appendBugProgress(cfg.StateDir, rid, "prepare", "failed", "Niuma", err.Error(), 0)
		a.onFailure(rec, "Bug worktree: "+err.Error(), "")
		return
	}
	appendBugProgress(cfg.StateDir, rid, "prepare", "done", "Niuma", "隔离目录已就绪："+branch, 0)
	a.writeBugDossier(wt, rid, rec)
	fix, review := resolveBugAgents(rec)
	a.noteAgent(rec, "code", fix)
	a.noteAgent(rec, "review", review)
	runner, err := os.Executable()
	if err != nil {
		a.onFailure(rec, "定位 niuma agent-run 失败: "+err.Error(), "")
		return
	}
	input := bugGraphInput{
		RecordID: rid, ThreadID: "bug:" + rid, Title: recTitle(rec),
		Description: fieldText(rec.Fields[FDesc]), Clarifications: fieldText(rec.Fields[FClarify]),
		Worktree: wt, BaseRef: isolated.BaseRef, TestCmd: isolated.TestCmd,
		FixAgent: fix, ReviewAgent: review, AgentRunner: runner,
		FixTimeout: cfg.TimeoutCode, ReviewTimeout: cfg.TimeoutReview,
		TestTimeout: cfg.TimeoutCode, MaxRepairs: cfg.BugRepairLimit,
		CheckpointDB: filepath.Join(cfg.StateDir, "buggraph.sqlite3"),
		ProgressFile: bugProgressPath(cfg.StateDir, rid),
	}
	a.fs.notify(chat, "🐛 Bug 流水线启动："+fix+" 调查/修复 → 测试 → "+review+" 独立 Review；改动位于 "+branch+"。")
	emit("dispatcher", "buggraph_start", map[string]any{"record_id": rid, "fix_agent": fix, "review_agent": review, "worktree": wt})
	output, err := runBugGraph(input, runID, a.st)
	if err != nil {
		appendBugProgress(cfg.StateDir, rid, "pipeline", "failed", "Niuma", err.Error(), 0)
		a.onFailure(rec, err.Error(), "")
		return
	}
	emit("dispatcher", "buggraph_done", map[string]any{"record_id": rid, "status": output.Status, "iteration": output.Iteration})
	switch output.Status {
	case "NEEDS_INPUT":
		questions := strings.TrimSpace(output.Questions)
		appendBugProgress(cfg.StateDir, rid, "investigate", "waiting", fix, questions, output.Iteration)
		merged := strings.TrimSpace(fieldText(rec.Fields[FClarify]) + "\n\n【Bug 流水线追问】\n" + questions)
		_ = a.advance(rec, SAnswer, "[bug] 调查需要人工补充", map[string]any{FClarify: merged})
		a.fs.notify(chat, "💬 修复 Bug 前还缺这些信息，直接回复即可：\n\n"+questions)
		return
	case "PASS":
		changed := productChangedFiles(changedFiles(isolated, wt))
		if len(changed) == 0 {
			a.blockBug(rec, "Review 虽返回 PASS，但 worktree 没有产品代码改动")
			return
		}
		appendBugProgress(cfg.StateDir, rid, "test", "running", "Niuma", "正在执行最终验收门", output.Iteration)
		ok, detail := a.runGate(isolated, wt)
		if !ok {
			appendBugProgress(cfg.StateDir, rid, "test", "failed", "Niuma", tail(detail, 12000), output.Iteration)
			a.blockBug(rec, "Go 最终验收门未通过："+tail(detail, 800))
			return
		}
		appendBugProgress(cfg.StateDir, rid, "test", "done", "Niuma", tail(detail, 12000), output.Iteration)
		appendBugProgress(cfg.StateDir, rid, "delivery", "running", "Niuma", "正在整理提交并准备交付分支", output.Iteration)
		a.gitMu.Lock()
		err = gitCommitAllChecked(wt, "[niuma] BUG-"+rid)
		a.gitMu.Unlock()
		if err != nil {
			appendBugProgress(cfg.StateDir, rid, "delivery", "failed", "Niuma", err.Error(), output.Iteration)
			a.blockBug(rec, "提交 Bug 修复失败："+err.Error())
			return
		}
		pub := afterDevelop(isolated, wt, branch)
		if !pub.OK {
			appendBugProgress(cfg.StateDir, rid, "delivery", "failed", "Niuma", pub.Detail, output.Iteration)
			a.blockBug(rec, "推送 Bug 分支失败："+pub.Detail)
			return
		}
		logLine := "[bug:" + fix + "→" + review + "] PASS，返修 " + itoa(output.Iteration) + " 轮，" + pub.Note + "，待人工合并"
		appendBugProgress(cfg.StateDir, rid, "delivery", "waiting", "Niuma", pub.Note+"；等待人工检查并合并", output.Iteration)
		_ = a.advance(rec, SMerge, logLine, map[string]any{FLink: orDefault(pub.Link, branch)})
		a.fs.notify(chat, "✅ Bug 修复并 Review 通过：改动 "+itoa(len(changed))+" 个文件；"+pub.Note+"。请人工检查并合并。")
		a.fs.notifyCard(chat, mergeCard(rec))
		return
	default:
		reason := strings.TrimSpace(output.Summary)
		if reason == "" {
			reason = "LangGraph 结束状态=" + output.Status
		}
		a.blockBug(rec, reason)
	}
}
