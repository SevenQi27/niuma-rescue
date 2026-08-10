package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type bugGraphInput struct {
	RecordID               string   `json:"record_id"`
	ThreadID               string   `json:"thread_id"`
	Title                  string   `json:"title"`
	Description            string   `json:"description"`
	Clarifications         string   `json:"clarifications"`
	Worktree               string   `json:"worktree"`
	BaseRef                string   `json:"base_ref"`
	TestCmd                string   `json:"test_cmd"`
	FixAgent               string   `json:"fix_agent"`
	ReviewAgent            string   `json:"review_agent"`
	AgentRunner            string   `json:"agent_runner"`
	FixTimeout             int      `json:"fix_timeout"`
	ReviewTimeout          int      `json:"review_timeout"`
	TestTimeout            int      `json:"test_timeout"`
	MaxRepairs             int      `json:"max_repairs"`
	CheckpointDB           string   `json:"checkpoint_db"`
	ProgressFile           string   `json:"progress_file"`
	SkipInvestigation      bool     `json:"skip_investigation,omitempty"`
	StopAfterInvestigation bool     `json:"stop_after_investigation,omitempty"`
	InitialDiagnosis       string   `json:"initial_diagnosis,omitempty"`
	InitialSessionID       string   `json:"initial_session_id,omitempty"`
	InitialAffectedFiles   []string `json:"initial_affected_files,omitempty"`
}

type bugGraphOutput struct {
	Status        string   `json:"status"`
	Summary       string   `json:"summary"`
	Diagnosis     string   `json:"diagnosis"`
	AffectedFiles []string `json:"affected_files"`
	FixSessionID  string   `json:"fix_session_id"`
	Questions     string   `json:"questions"`
	TestOK        bool     `json:"test_ok"`
	TestOutput    string   `json:"test_output"`
	ReviewOutput  string   `json:"review_output"`
	Iteration     int      `json:"iteration"`
}

func runBugGraph(parent context.Context, input bugGraphInput, runID string, st *Store) (bugGraphOutput, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return bugGraphOutput{}, err
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(cfg.TimeoutBug)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.BugGraphPython, "-m", "buggraph")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = cfg.BugGraphDir
	env := workspaceToolEnvForDir(scrubbedEnv(), input.Worktree)
	env = replaceEnv(env, "PYTHONPATH", filepath.Join(cfg.BugGraphDir, "src"))
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
				if errors.Is(ctx.Err(), context.Canceled) {
					return bugGraphOutput{}, context.Canceled
				}
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
			if errors.Is(ctx.Err(), context.Canceled) {
				return bugGraphOutput{}, context.Canceled
			}
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
	a.releaseBugCoordination(rec.RecordID, "failed")
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
	runCtx, stop := context.WithCancel(context.Background())
	if _, loaded := a.bugRuns.LoadOrStore(rid, stop); loaded {
		stop()
		a.blockBug(rec, "同一 Bug 已有正在运行的任务")
		return
	}
	defer func() {
		stop()
		a.bugRuns.Delete(rid)
	}()
	coordination, hasCoordination := a.st.bugCoordination(rid)
	if !hasCoordination {
		resetBugProgress(cfg.StateDir, rid)
	}
	appendBugProgress(cfg.StateDir, rid, "prepare", "running", "Niuma", "正在从开发会话快照准备任务修复目录", 0)
	if !strings.EqualFold(ws.SCM, "git") {
		a.blockBug(rec, "Bug 最小链路当前只支持 git workspace")
		return
	}
	a.gitMu.Lock()
	prepared, err := a.prepareCodeTask(ws, "BUG", rid)
	a.gitMu.Unlock()
	if err != nil {
		appendBugProgress(cfg.StateDir, rid, "prepare", "failed", "Niuma", err.Error(), 0)
		a.onFailure(rec, "Bug worktree: "+err.Error(), "")
		return
	}
	isolated, wt, branch := prepared.Workspace, prepared.Worktree, prepared.Branch
	if errors.Is(runCtx.Err(), context.Canceled) {
		a.releaseBugCoordination(rid, "cancelled")
		_ = a.advance(rec, SBlocked, "[manual] 已从管理页停止任务 → 已阻塞", map[string]any{})
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
		Worktree: wt, BaseRef: prepared.CompareRef, TestCmd: isolated.TestCmd,
		FixAgent: fix, ReviewAgent: review, AgentRunner: runner,
		FixTimeout: cfg.TimeoutCode, ReviewTimeout: cfg.TimeoutReview,
		TestTimeout: cfg.TimeoutCode, MaxRepairs: cfg.BugRepairLimit,
		CheckpointDB: filepath.Join(cfg.StateDir, "buggraph.sqlite3"),
		ProgressFile: bugProgressPath(cfg.StateDir, rid),
	}
	if !hasCoordination || strings.TrimSpace(coordination.Diagnosis) == "" {
		input.StopAfterInvestigation = true
		investigation, investigateErr := runBugGraph(runCtx, input, runID, a.st)
		if investigateErr != nil {
			if errors.Is(investigateErr, context.Canceled) {
				appendBugProgress(cfg.StateDir, rid, "pipeline", "failed", "Niuma", "任务已由管理页人工停止", 0)
				_ = a.advance(rec, SBlocked, "[manual] 已从管理页停止任务 → 已阻塞", map[string]any{})
				return
			}
			appendBugProgress(cfg.StateDir, rid, "pipeline", "failed", "Niuma", investigateErr.Error(), 0)
			a.onFailure(rec, investigateErr.Error(), "")
			return
		}
		switch investigation.Status {
		case "NEEDS_INPUT":
			questions := strings.TrimSpace(investigation.Questions)
			merged := strings.TrimSpace(fieldText(rec.Fields[FClarify]) + "\n\n【Bug 流水线追问】\n" + questions)
			_ = a.advance(rec, SAnswer, "[bug] 调查需要人工补充", map[string]any{FClarify: merged})
			a.fs.notify(chat, "💬 修复 Bug 前还缺这些信息，直接回复即可：\n\n"+questions)
			return
		case "DIAGNOSED":
			// 继续进入代码范围协调。
		default:
			reason := strings.TrimSpace(investigation.Summary)
			if reason == "" {
				reason = "Bug 调查未形成可执行结论"
			}
			a.blockBug(rec, reason)
			return
		}
		similarID, similarity := a.bugSimilarityCandidate(rec, ws)
		coordination = bugCoordinationRow{
			RecordID: rid, Workspace: ws.Key, Branch: branch, State: "diagnosed",
			Diagnosis: investigation.Diagnosis, Files: investigation.AffectedFiles,
			SessionID: investigation.FixSessionID, SimilarRecord: similarID, Similarity: similarity,
		}
		if err := a.st.saveBugCoordination(coordination); err != nil {
			a.blockBug(rec, "保存代码范围调查结果失败："+err.Error())
			return
		}
	}
	coordination.Workspace, coordination.Branch = ws.Key, branch
	appendBugProgress(cfg.StateDir, rid, "coordinate", "running", "Niuma", "正在检查相似任务和代码文件占用", 0)
	decision := bugClaimDecision{}
	if coordination.State == "ready" && coordination.PredecessorID != "" {
		predecessor, ok := a.st.bugCoordination(coordination.PredecessorID)
		if ok && predecessor.Branch != "" {
			decision = bugClaimDecision{PredecessorID: predecessor.RecordID, Branch: predecessor.Branch}
		} else {
			a.st.resetBugDependency(rid)
			coordination.State, coordination.PredecessorID = "diagnosed", ""
		}
	}
	if decision.PredecessorID == "" {
		if ws.sharedSession() {
			decision = bugClaimDecision{Allowed: true}
			a.st.markBugCoordination(rid, "editing")
		} else {
			decision, err = a.st.claimBugFiles(coordination)
		}
	}
	if err != nil {
		a.blockBug(rec, "检查代码区域占用失败："+err.Error())
		return
	}
	if !decision.Allowed && decision.WaitReason != "" {
		detail := decision.WaitReason + "；后续将按 " + decision.PredecessorID + " → " + rid + " 的顺序合并"
		appendBugProgress(cfg.StateDir, rid, "coordinate", "waiting", "Niuma", detail, 0)
		_ = a.advance(rec, SCodeWait, "[coordination] "+detail, map[string]any{})
		a.fs.notify(chat, "⏳ Bug「"+recTitle(rec)+"」与 "+decision.PredecessorID+" 修改同一代码区域；调查结果已保留，前置任务 Review 通过后会自动继续。")
		return
	}
	if !decision.Allowed && decision.PredecessorID != "" {
		a.gitMu.Lock()
		stackErr := scmStackBugBranch(wt, decision.Branch)
		a.gitMu.Unlock()
		if stackErr != nil {
			a.blockBug(rec, "接入前置任务 "+decision.PredecessorID+" 失败："+stackErr.Error())
			return
		}
		if err := a.st.takeBugFiles(coordination, decision.PredecessorID); err != nil {
			a.blockBug(rec, "接管前置任务代码区域失败："+err.Error())
			return
		}
		appendBugProgress(cfg.StateDir, rid, "coordinate", "done", "Niuma",
			"已接在 "+decision.PredecessorID+" 的 Review 分支之后；合并顺序："+decision.PredecessorID+" → "+rid, 0)
	} else {
		files := coordination.Files
		if len(files) == 1 && files[0] == unknownBugScope {
			appendBugProgress(cfg.StateDir, rid, "coordinate", "done", "Niuma", "未能精确定位文件，已按工作区保守占用", 0)
		} else if ws.sharedSession() {
			appendBugProgress(cfg.StateDir, rid, "coordinate", "done", "Niuma",
				"任务在临时 worktree 并行开发；完成后将串行集成到共享会话", 0)
		} else {
			appendBugProgress(cfg.StateDir, rid, "coordinate", "done", "Niuma", "已登记 "+itoa(len(files))+" 个预计修改文件", 0)
		}
	}
	input.SkipInvestigation = true
	input.StopAfterInvestigation = false
	input.InitialDiagnosis = coordination.Diagnosis
	input.InitialSessionID = coordination.SessionID
	input.InitialAffectedFiles = coordination.Files
	a.fs.notify(chat, "🐛 Bug 流水线启动："+fix+" 调查/修复 → 测试 → "+review+" 独立 Review；改动位于 "+branch+"。")
	emit("dispatcher", "buggraph_start", map[string]any{"record_id": rid, "fix_agent": fix, "review_agent": review, "worktree": wt})
	output, err := runBugGraph(runCtx, input, runID, a.st)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			a.releaseBugCoordination(rid, "cancelled")
			appendBugProgress(cfg.StateDir, rid, "pipeline", "failed", "Niuma", "任务已由管理页人工停止", 0)
			_ = a.advance(rec, SBlocked, "[manual] 已从管理页停止任务 → 已阻塞", map[string]any{})
			return
		}
		appendBugProgress(cfg.StateDir, rid, "pipeline", "failed", "Niuma", err.Error(), 0)
		a.onFailure(rec, err.Error(), "")
		if fieldText(rec.Fields[FStatus]) == SBlocked {
			a.releaseBugCoordination(rid, "failed")
		}
		return
	}
	emit("dispatcher", "buggraph_done", map[string]any{"record_id": rid, "status": output.Status, "iteration": output.Iteration})
	switch output.Status {
	case "NEEDS_INPUT":
		a.releaseBugCoordination(rid, "waiting_input")
		questions := strings.TrimSpace(output.Questions)
		appendBugProgress(cfg.StateDir, rid, "investigate", "waiting", fix, questions, output.Iteration)
		merged := strings.TrimSpace(fieldText(rec.Fields[FClarify]) + "\n\n【Bug 流水线追问】\n" + questions)
		_ = a.advance(rec, SAnswer, "[bug] 调查需要人工补充", map[string]any{FClarify: merged})
		a.fs.notify(chat, "💬 修复 Bug 前还缺这些信息，直接回复即可：\n\n"+questions)
		return
	case "PASS":
		changed := productChangedFiles(changedFiles(isolated, wt))
		if prepared.SessionID != "" {
			changed = productChangedFiles(changedFilesFrom(wt, prepared.CompareRef))
		}
		if len(changed) == 0 {
			a.blockBug(rec, "Review 虽返回 PASS，但 worktree 没有产品代码改动")
			return
		}
		coveredByPredecessor := prepared.SessionID == "" && coordination.PredecessorID != "" && len(productWorkingTreeChanges(wt)) == 0
		appendBugProgress(cfg.StateDir, rid, "test", "running", "Niuma", "正在执行最终验收门", output.Iteration)
		ok, detail := a.runGate(isolated, wt)
		if !ok {
			appendBugProgress(cfg.StateDir, rid, "test", "failed", "Niuma", tail(detail, 12000), output.Iteration)
			a.blockBug(rec, "Go 最终验收门未通过："+tail(detail, 800))
			return
		}
		appendBugProgress(cfg.StateDir, rid, "test", "done", "Niuma", tail(detail, 12000), output.Iteration)
		appendBugProgress(cfg.StateDir, rid, "delivery", "running", "Niuma", "正在整理提交并准备交付分支", output.Iteration)
		if !coveredByPredecessor {
			a.gitMu.Lock()
			_, err = a.commitDevelopmentTask(prepared, rid, "[niuma] BUG-"+rid)
			a.gitMu.Unlock()
			if err != nil {
				appendBugProgress(cfg.StateDir, rid, "delivery", "failed", "Niuma", err.Error(), output.Iteration)
				a.blockBug(rec, "提交 Bug 修复失败："+err.Error())
				return
			}
		}
		publishWT, publishBranch := wt, branch
		if prepared.SessionID != "" {
			a.gitMu.Lock()
			session, integrationErr := a.integrateDevelopmentTask(prepared, rid)
			a.gitMu.Unlock()
			if integrationErr != nil {
				appendBugProgress(cfg.StateDir, rid, "delivery", "failed", "Niuma", integrationErr.Error(), output.Iteration)
				a.blockBug(rec, "进入共享开发会话失败："+integrationErr.Error())
				return
			}
			publishWT, publishBranch = session.Worktree, session.Branch
			branch = session.Branch
		}
		pub := afterDevelop(isolated, publishWT, publishBranch)
		if !pub.OK {
			appendBugProgress(cfg.StateDir, rid, "delivery", "failed", "Niuma", pub.Detail, output.Iteration)
			a.blockBug(rec, "推送 Bug 分支失败："+pub.Detail)
			return
		}
		logLine := "[bug:" + fix + "→" + review + "] PASS，返修 " + itoa(output.Iteration) + " 轮，" + pub.Note + "，待人工合并"
		if coveredByPredecessor {
			logLine = "[coordination] 当前 Bug 已由前置任务 " + coordination.PredecessorID + " 的补丁覆盖并通过独立 Review；" + pub.Note + "，请按依赖顺序确认"
		}
		appendBugProgress(cfg.StateDir, rid, "delivery", "waiting", "Niuma", pub.Note+"；等待人工检查并合并", output.Iteration)
		a.st.markBugCoordination(rid, "reviewed")
		_ = a.advance(rec, SMerge, logLine, map[string]any{FLink: orDefault(pub.Link, branch)})
		a.wakeBugDependents(rid, true)
		if coveredByPredecessor {
			a.fs.notify(chat, "✅ Bug 已由前置任务 "+coordination.PredecessorID+" 的补丁覆盖并通过 Review；"+pub.Note+"。请按页面显示顺序确认合并。")
		} else {
			a.fs.notify(chat, "✅ Bug 修复并 Review 通过：改动 "+itoa(len(changed))+" 个文件；"+pub.Note+"。请人工检查并合并。")
		}
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

func (a *App) stopBug(recordID string) bool {
	value, ok := a.bugRuns.Load(recordID)
	if !ok {
		return false
	}
	value.(context.CancelFunc)()
	return true
}
