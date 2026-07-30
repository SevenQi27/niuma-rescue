package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type App struct {
	fs      *Feishu
	st      *Store
	sem     chan struct{} // 并发上限
	gitMu   sync.Mutex    // 串行化共享 base 仓库上的 worktree 操作
	wsLocks sync.Map      // workspace key -> *sync.Mutex：inline 工作区共享同一棵树，按工作区串行
}

// wsLock 返回某工作区的互斥锁（懒创建）。inline 模式下同一工作区同一时刻只允许一条/一批需求在跑。
func (a *App) wsLock(key string) *sync.Mutex {
	m, _ := a.wsLocks.LoadOrStore(key, &sync.Mutex{})
	return m.(*sync.Mutex)
}

var agentLog = func(s string) { logf("%s", s) }

// ── 状态推进 / 失败 ───────────────────────────────────────────────────
func (a *App) advance(rec *Record, status, logLine string, extra map[string]any) error {
	cur := fieldText(rec.Fields[FStatus])
	if status != cur && !ValidTransitions[cur][status] {
		return errf("非法状态流转：%s -> %s", cur, status)
	}
	prev := fieldText(rec.Fields[FLog])
	newLog := strings.TrimSpace(prev+logLine+"\n") + "\n"
	fields := map[string]any{FStatus: status, FLog: newLog}
	for k, v := range extra {
		fields[k] = v
	}
	if err := a.fs.updateRecord(rec.RecordID, fields); err != nil {
		return err
	}
	for k, v := range fields {
		rec.Fields[k] = v
	}
	emit("dispatcher", "transition", map[string]any{"record_id": rec.RecordID, "from": cur, "to": status})
	return nil
}

func (a *App) onFailure(rec *Record, msg, retryStatus string) {
	fails := fieldInt(rec.Fields[FFails]) + 1
	line := fmt.Sprintf("[fail#%d] %s", fails, msg)
	chat := fieldText(rec.Fields[FChat])
	switch {
	case fails >= cfg.FailureLimit:
		a.advance(rec, SBlocked, line+" → 已阻塞", map[string]any{FFails: fails})
		a.fs.notifyCard(chat, blockedCard(rec, msg))
		a.fs.notify(chat, "⚠️ 需求「"+fieldText(rec.Fields[FTitle])+"」已阻塞，需人工介入。原因："+trunc(msg, 200))
	case retryStatus != "" && retryStatus != fieldText(rec.Fields[FStatus]):
		a.advance(rec, retryStatus, line+" → 回到"+retryStatus, map[string]any{FFails: fails})
	default:
		prev := fieldText(rec.Fields[FLog])
		newLog := strings.TrimSpace(prev+line+"\n") + "\n"
		a.fs.updateRecord(rec.RecordID, map[string]any{FFails: fails, FLog: newLog})
		rec.Fields[FFails] = fails
		rec.Fields[FLog] = newLog
	}
}

func (a *App) retryDelay(fails int) int {
	if fails < 1 {
		fails = 1
	}
	d := cfg.RetryBase << (fails - 1)
	if d > 900 {
		d = 900
	}
	return d
}

func (a *App) resolveAgent(rec *Record, stage, def string) string {
	field := map[string]string{"clarify": FAgentClarify, "code": FAgentCode, "review": FAgentReview}[stage]
	raw := fieldText(rec.Fields[field])
	if raw == "" {
		raw = fieldText(rec.Fields[FAgent])
	}
	if raw == "" {
		return def
	}
	e := normalizeAgent(raw)
	if _, ok := AgentCmds[e]; !ok {
		return def
	}
	return e
}

func (a *App) workspaceFor(rec *Record) Workspace {
	ws, err := workspaceGet(fieldText(rec.Fields[FWorkspace]))
	if err != nil {
		ws, _ = workspaceGet("")
	}
	return ws
}

func (a *App) dossierDir(wt, reqID string) string {
	d := filepath.Join(wt, dossierDir, "REQ-"+reqID)
	os.MkdirAll(d, 0o755)
	return d
}

func (a *App) writeDossier(wt, reqID string, f map[string]any) {
	d := a.dossierDir(wt, reqID)
	os.WriteFile(filepath.Join(d, "prd.md"), []byte(fieldText(f[FPRD])), 0o644)
	os.WriteFile(filepath.Join(d, "requirement.md"), []byte(fieldText(f[FDesc])), 0o644)
}

// ── 进度回调（心跳 + 飞书卡片）────────────────────────────────────────
func (a *App) makeProgress(chatID, title, stage, engine, runID string) func(map[string]any) {
	wantCard := chatID != "" && cfg.ProgressInterval > 0
	var mid string
	var lastCard, lastHB float64
	start := time.Now()
	return func(stats map[string]any) {
		el := time.Since(start).Seconds()
		if runID != "" && el-lastHB >= 15 {
			lastHB = el
			a.st.heartbeat(runID)
		}
		if !wantCard {
			return
		}
		if mid != "" && el-lastCard < float64(cfg.ProgressInterval) {
			return
		}
		c := progressCard(title, stage, engine, stats)
		if mid == "" {
			if m, err := a.fs.sendCard(chatID, c); err == nil {
				mid = m
			}
		} else {
			a.fs.patchCard(mid, c)
		}
		lastCard = el
	}
}

// ── 阶段处理器 ────────────────────────────────────────────────────────
func (a *App) handleClarify(rec *Record, runID string) {
	ws := a.workspaceFor(rec)
	if ws.inline() && cfg.BatchClarify {
		a.handleClarifyInlineBatch(rec, ws, runID)
		return
	}
	a.clarifyOne(rec, ws, runID)
}

// clarifyOne：单条澄清（worktree 模式或关闭合批时）。
func (a *App) clarifyOne(rec *Record, ws Workspace, runID string) {
	f := rec.Fields
	chat := fieldText(f[FChat])
	engine := a.resolveAgent(rec, "clarify", cfg.EngineClarify)
	a.noteAgent(rec, "clarify", engine)
	prompt := buildPrompt("clarify", map[string]string{"requirement": fieldText(f[FDesc]), "clarifications": fieldText(f[FClarify])})
	a.fs.notify(chat, "🤔 正在澄清需求（"+engine+" · "+ws.Key+"）…")
	prog := a.makeProgress(chat, recTitle(rec), "澄清", engine, runID)
	res := runAgent(engine, prompt, ws.Path, cfg.TimeoutClarify, agentLog, prog)
	if !res.OK {
		a.onFailure(rec, "clarify("+engine+") 调用失败: "+trunc(res.Output, 300), "")
		return
	}
	a.applyClarifyVerdict(rec, engine, res.Output)
}

// applyClarifyVerdict：把一条澄清的产出路由到「待确认」或「待回答」。
func (a *App) applyClarifyVerdict(rec *Record, engine, output string) {
	chat := fieldText(rec.Fields[FChat])
	verdict, rest := routeClarify(output)
	if verdict == "CLEAR" {
		a.advance(rec, SConfirm, "[clarify:"+engine+"] 信息充分，PRD 已生成，待人确认", map[string]any{FPRD: rest})
		a.fs.notifyCard(chat, confirmCard(rec))
	} else {
		merged := strings.TrimSpace(fieldText(rec.Fields[FClarify]) + "\n\n" + strings.TrimSpace(output))
		a.advance(rec, SAnswer, "[clarify:"+engine+"] 产出澄清问题，待人回答", map[string]any{FClarify: merged})
		a.fs.notify(chat, "🤔 关于「"+recTitle(rec)+"」我有几个问题，直接回复我即可：\n\n"+strings.TrimSpace(output))
	}
}

// handleClarifyInlineBatch：同工作区的「待澄清」需求合批，一次 agent 调用逐条产出，再按 @@@REQ:<id> 切分路由。
// 调用方已持有该工作区的锁。
func (a *App) handleClarifyInlineBatch(rec *Record, ws Workspace, runID string) {
	batch := a.collectClarifyBatch(rec, ws)
	if len(batch) == 0 {
		batch = []*Record{rec}
	}
	engine := a.resolveAgent(rec, "clarify", cfg.EngineClarify)
	chat := fieldText(rec.Fields[FChat])
	for _, r := range batch {
		a.noteAgent(r, "clarify", engine)
	}

	var sb strings.Builder
	sb.WriteString("你是需求澄清专家。下面是同一工作区的多条需求，逐条判断信息是否够清楚能直接开干。\n\n")
	sb.WriteString("# 输出格式（务必严格：系统按 `@@@REQ:<id>` 行切分逐条处理，别的内容一律不要）\n")
	sb.WriteString("对每条需求，先单独输出一行 `@@@REQ:<原样id>`，紧接着：\n")
	sb.WriteString("- 信息够了：下一行 `CLEAR`，再跟一份简短 PRD（## 目标 / ## 范围 / ## 验收标准 / ## 预计改动模块）。\n")
	sb.WriteString("- 还有阻塞性疑问：下一行 `QUESTIONS`，再跟**最多 3 个**问题，一句一个、说人话、别注水。\n")
	sb.WriteString("不要开场白、不要总结、不要把多条混在一起。\n\n")
	sb.WriteString("# 需求列表\n")
	for _, r := range batch {
		sb.WriteString("@@@REQ:" + r.RecordID + "\n")
		sb.WriteString("标题：" + recTitle(r) + "\n")
		sb.WriteString("需求原文：" + fieldText(r.Fields[FDesc]) + "\n")
		if c := strings.TrimSpace(fieldText(r.Fields[FClarify])); c != "" {
			sb.WriteString("已有澄清问答：" + c + "\n")
		}
		sb.WriteString("\n")
	}

	titles := make([]string, len(batch))
	for i, r := range batch {
		titles[i] = recTitle(r)
	}
	a.fs.notify(chat, fmt.Sprintf("🤔 正在澄清本批 %d 个需求（%s · %s）：%s…", len(batch), engine, ws.Key, strings.Join(titles, " / ")))
	emit("dispatcher", "batch_clarify_start", map[string]any{"workspace": ws.Key, "count": len(batch), "engine": engine})

	prog := a.makeProgress(chat, fmt.Sprintf("澄清×%d", len(batch)), "澄清", engine, runID)
	res := runAgent(engine, sb.String(), ws.Path, cfg.TimeoutClarify, agentLog, prog)
	if !res.OK {
		for _, r := range batch {
			a.onFailure(r, "clarify("+engine+") 合批调用失败: "+trunc(res.Output, 300), "")
		}
		return
	}

	blocks := parseClarifyBatch(res.Output)
	for _, r := range batch {
		out, ok := blocks[r.RecordID]
		if !ok || strings.TrimSpace(out) == "" {
			a.onFailure(r, "合批澄清输出里没找到本需求的块（agent 未按 @@@REQ 格式输出）", "")
			continue
		}
		a.applyClarifyVerdict(r, engine, out)
	}
}

// collectClarifyBatch 收集与 rec 同工作区、状态为「待澄清」的所有需求（含 rec）。
func (a *App) collectClarifyBatch(rec *Record, ws Workspace) []*Record {
	records, err := a.fs.listRecords()
	if err != nil {
		return []*Record{rec}
	}
	var batch []*Record
	seen := map[string]bool{rec.RecordID: true}
	batch = append(batch, rec)
	for i := range records {
		r := &records[i]
		if seen[r.RecordID] || fieldText(r.Fields[FStatus]) != SClarify {
			continue
		}
		if a.workspaceFor(r).Key == ws.Key {
			seen[r.RecordID] = true
			batch = append(batch, r)
		}
	}
	return batch
}

// parseClarifyBatch 按 `@@@REQ:<id>` 行把合批澄清输出切成 record_id -> 块内容。
func parseClarifyBatch(output string) map[string]string {
	res := map[string]string{}
	var curID string
	var buf []string
	flush := func() {
		if curID != "" {
			res[curID] = strings.TrimSpace(strings.Join(buf, "\n"))
		}
		buf = nil
	}
	for _, ln := range strings.Split(output, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "@@@REQ:") {
			flush()
			curID = strings.TrimSpace(strings.TrimPrefix(t, "@@@REQ:"))
			continue
		}
		if curID != "" {
			buf = append(buf, ln)
		}
	}
	flush()
	return res
}

// noteAgent 把实际用到的 agent 回写到阶段字段（仅当字段原本为空），方便在飞书表格里看到。
func (a *App) noteAgent(rec *Record, stage, engine string) {
	field := map[string]string{"clarify": FAgentClarify, "code": FAgentCode, "review": FAgentReview}[stage]
	if field == "" || fieldText(rec.Fields[field]) != "" {
		return
	}
	if err := a.fs.updateRecord(rec.RecordID, map[string]any{field: engine}); err == nil {
		rec.Fields[field] = engine
	}
}

func (a *App) handleDevelop(rec *Record, runID string) {
	ws := a.workspaceFor(rec)
	if ws.inline() {
		a.handleDevelopInline(rec, ws, runID)
		return
	}
	rid := rec.RecordID
	f := rec.Fields
	chat := fieldText(f[FChat])
	a.gitMu.Lock()
	wt, branch, err := scmPrepare(ws, rid)
	a.gitMu.Unlock()
	if err != nil {
		a.onFailure(rec, "worktree: "+err.Error(), "")
		return
	}
	a.writeDossier(wt, rid, f)
	engine := a.resolveAgent(rec, "code", cfg.EngineCode)
	a.noteAgent(rec, "code", engine)
	changed := productChangedFiles(changedFiles(ws, wt))
	if len(changed) == 0 {
		a.fs.notify(chat, "🔧 开始开发（"+engine+"）：写代码 + 跑测试，可能需要几分钟，请稍候…")
		prog := a.makeProgress(chat, recTitle(rec), "开发", engine, runID)
		prompt := buildPrompt("code", map[string]string{"req_id": rid, "dossier": a.dossierDir(wt, rid)})
		res := runAgent(engine, prompt, wt, cfg.TimeoutCode, agentLog, prog)
		changed = productChangedFiles(changedFiles(ws, wt))
		if !res.OK {
			if len(changed) == 0 {
				a.onFailure(rec, "coder("+engine+") 调用失败且没有产生改动: "+trunc(res.Output, 500), "")
				return
			}
			a.fs.notify(chat, "⚠️ "+engine+" 返回失败，但检测到已产生 "+itoa(len(changed))+" 个文件改动；继续执行验收门。\n"+trunc(res.Output, 300))
			emit("dispatcher", "agent_failed_with_changes", map[string]any{"record_id": rid, "engine": engine, "changed": len(changed)})
		}
		if !ws.inline() {
			a.gitMu.Lock()
			gitCommitAll(wt, "[niuma] REQ-"+rid)
			a.gitMu.Unlock()
		}
		changed = productChangedFiles(changedFiles(ws, wt))
	}
	okTest, detail := a.runGate(ws, wt)
	emit("dispatcher", "gate_done", map[string]any{"record_id": rid, "ok": okTest})
	if !okTest {
		a.fs.notify(chat, "❌ 测试未通过，将重试（"+engine+"）。")
		a.onFailure(rec, "测试未通过: "+tail(detail, 400), "")
		return
	}
	pub := afterDevelop(ws, wt, branch)
	if !pub.OK {
		a.onFailure(rec, "发布失败: "+pub.Detail, "")
		return
	}
	a.advance(rec, SReview, "[code:"+engine+"] 完成、测试通过、"+pub.Note, map[string]any{FLink: pub.Link})
	a.fs.notify(chat, "✅ 开发完成（"+engine+"）：改动 "+itoa(len(changed))+" 个文件 · 测试通过 · "+pub.Note+"，进入 Review。")
}

// handleDevelopInline：inline 工作区的开发。多条「开发中」需求合批成一次 agent 调用，
// 在共享工作树上一次性实现；跳过自动测试门（可配置）；完成后每条停在「待合并」，
// 推卡片让人决定「做 Review」还是「标记完成」。调用方已持有该工作区的锁。
func (a *App) handleDevelopInline(rec *Record, ws Workspace, runID string) {
	wt := ws.Path
	batch := a.collectDevBatch(rec, ws)
	if len(batch) == 0 {
		batch = []*Record{rec}
	}
	engine := a.resolveAgent(rec, "code", cfg.EngineCode)
	chat := fieldText(rec.Fields[FChat])
	for _, r := range batch {
		a.noteAgent(r, "code", engine)
	}

	// 写每条需求的档案 + 拼合批 prompt
	var sb strings.Builder
	sb.WriteString("你是资深工程师。本批次需要在同一个工作区里，一次性实现下面这些需求。\n")
	sb.WriteString("仓库根的 AGENTS.md 是代码规范与构建/测试约定的唯一来源，务必遵守。\n\n")
	for i, r := range batch {
		a.writeDossier(wt, r.RecordID, r.Fields)
		sb.WriteString(fmt.Sprintf("## 需求 %d：REQ-%s %s\n- 档案目录：%s/（先读 prd.md，再读 requirement.md）\n\n",
			i+1, r.RecordID, recTitle(r), a.dossierDir(wt, r.RecordID)))
	}
	sb.WriteString("# 要求\n")
	sb.WriteString("1. 逐个实现每条需求的验收标准，改动控制在各自 PRD 的范围内，不顺手重构无关代码。\n")
	sb.WriteString("2. 为关键逻辑补单元测试。\n")
	sb.WriteString("3. inline 策略：改动留在工作区即可，**绝对不要 commit / push**，由人工决定提交。\n")
	sb.WriteString("4. 每条需求在其档案目录写 handoff.json：{\"stage\":\"coder\",\"done\":\"...\",\"files_touched\":[...]}。\n")
	sb.WriteString("5. 不碰密钥 / .env；输入在边界处校验。\n")

	titles := make([]string, len(batch))
	for i, r := range batch {
		titles[i] = recTitle(r)
	}
	a.fs.notify(chat, fmt.Sprintf("🔧 开始开发（%s · %s）：本批 %d 个需求 [%s]，单次跑通，请稍候…",
		engine, ws.Key, len(batch), strings.Join(titles, " / ")))
	emit("dispatcher", "batch_develop_start", map[string]any{"workspace": ws.Key, "count": len(batch), "engine": engine})

	prog := a.makeProgress(chat, fmt.Sprintf("批次×%d", len(batch)), "开发", engine, runID)
	res := runAgent(engine, sb.String(), wt, cfg.TimeoutCode, agentLog, prog)
	changed := productChangedFiles(changedFiles(ws, wt))
	if !res.OK && len(changed) == 0 {
		for _, r := range batch {
			a.onFailure(r, "coder("+engine+") 调用失败且没有产生改动: "+trunc(res.Output, 400), "")
		}
		return
	}
	if !res.OK {
		a.fs.notify(chat, "⚠️ "+engine+" 返回失败，但检测到已产生 "+itoa(len(changed))+" 个文件改动；继续。\n"+trunc(res.Output, 300))
	}

	// 验收门：inline 默认跳过，可用 PIPELINE_INLINE_SKIP_GATE=0 打开
	gateNote := "（inline 已跳过自动测试门）"
	if !cfg.InlineSkipGate {
		ok, detail := a.runGate(ws, wt)
		emit("dispatcher", "gate_done", map[string]any{"workspace": ws.Key, "ok": ok})
		if !ok {
			for _, r := range batch {
				a.onFailure(r, "测试未通过: "+tail(detail, 300), "")
			}
			a.fs.notify(chat, "❌ 测试未通过，本批回退重试。")
			return
		}
		gateNote = "测试通过"
	}

	for _, r := range batch {
		a.advance(r, SMerge, "[code:"+engine+"] 批次开发完成（共 "+itoa(len(batch))+" 项），"+gateNote+"，待人工决定 Review/合并", nil)
		a.fs.notifyCard(fieldText(r.Fields[FChat]), reviewDecisionCard(r, len(changed)))
	}
	a.fs.notify(chat, fmt.Sprintf("✅ 本批 %d 个需求开发完成（%s）：共改动 %d 个文件，%s。点卡片决定是否 Review。",
		len(batch), engine, len(changed), gateNote))
}

// collectDevBatch 收集与 rec 同工作区、状态为「开发中」的所有需求（含 rec）。
// BatchDevelop 关闭时只返回 rec。
func (a *App) collectDevBatch(rec *Record, ws Workspace) []*Record {
	if !cfg.BatchDevelop {
		return []*Record{rec}
	}
	records, err := a.fs.listRecords()
	if err != nil {
		return []*Record{rec}
	}
	var batch []*Record
	seen := map[string]bool{}
	add := func(r *Record) {
		if !seen[r.RecordID] {
			seen[r.RecordID] = true
			batch = append(batch, r)
		}
	}
	add(rec)
	for i := range records {
		r := &records[i]
		if fieldText(r.Fields[FStatus]) != SDev {
			continue
		}
		if a.workspaceFor(r).Key == ws.Key {
			add(r)
		}
	}
	return batch
}

func (a *App) handleReview(rec *Record, runID string) {
	rid := rec.RecordID
	f := rec.Fields
	chat := fieldText(f[FChat])
	ws := a.workspaceFor(rec)
	a.gitMu.Lock()
	wt, branch, err := scmPrepare(ws, rid)
	a.gitMu.Unlock()
	if err != nil {
		a.onFailure(rec, "worktree: "+err.Error(), "")
		return
	}
	engine := a.resolveAgent(rec, "review", cfg.EngineReview)
	a.noteAgent(rec, "review", engine)
	prompt := buildPrompt("review", map[string]string{"dossier": a.dossierDir(wt, rid), "diff": diffText(ws, wt)})
	a.fs.notify(chat, "🔍 开始 Review（"+engine+"）：审查改动中…")
	prog := a.makeProgress(chat, recTitle(rec), "Review", engine, runID)
	res := runAgent(engine, prompt, wt, cfg.TimeoutReview, agentLog, prog)
	if !res.OK {
		a.onFailure(rec, "reviewer("+engine+") 调用失败: "+trunc(res.Output, 300), "")
		return
	}
	if reviewVerdict(res.Output) == "PASS" {
		title := fieldText(f[FTitle])
		if title == "" {
			title = branch
		}
		pub := afterReview(ws, wt, branch, title, fieldText(f[FPRD]))
		if !pub.OK {
			a.onFailure(rec, "发布失败: "+pub.Detail, "")
			return
		}
		link := pub.Link
		if link == "" {
			link = fieldText(f[FLink])
		}
		if link == "" {
			link = branch
		}
		a.advance(rec, SMerge, "[review:"+engine+"] PASS，"+pub.Note+"，待人工合并", map[string]any{FLink: link})
		a.fs.notify(chat, "✅ Review 通过（"+engine+"）！"+pub.Note+"。")
		a.fs.notifyCard(chat, mergeCard(rec))
	} else {
		a.fs.notify(chat, "🛠 Review 未通过，打回开发：\n"+trunc(res.Output, 500))
		a.onFailure(rec, "Review 未过:\n"+trunc(res.Output, 400), SDev)
	}
}

// ── 验收门（相对基线）─────────────────────────────────────────────────
func anyCodeFile(files []string) bool {
	for _, fpath := range files {
		for _, ext := range CodeExts {
			if strings.HasSuffix(fpath, ext) {
				return true
			}
		}
	}
	return false
}

func runTest(cmdStr, dir string) (int, string) {
	cmd := exec.Command("sh", "-c", cmdStr)
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	return cmd.ProcessState.ExitCode(), string(out)
}

func errorCount(out string) int {
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if GateErrorRe.MatchString(l) {
			n++
		}
	}
	return n
}

func (a *App) runGate(ws Workspace, wt string) (bool, string) {
	if strings.TrimSpace(ws.TestCmd) == "" {
		return true, "(未设验收门)"
	}
	if !anyCodeFile(changedFiles(ws, wt)) {
		return true, "纯非代码改动，跳过验收门"
	}
	afterRC, afterOut := runTest(ws.TestCmd, wt)
	if afterRC == 0 {
		return true, "验收门通过（绿）"
	}
	if !cfg.GateRelative {
		return false, tail(afterOut, 400)
	}
	baseRC, baseOut, ok := a.baselineRun(ws)
	if !ok {
		return false, "验收失败且无法建立基线对比：\n" + tail(afterOut, 400)
	}
	if baseRC == 0 {
		return false, "基线本是通过的，本次改动引入了失败：\n" + tail(afterOut, 400)
	}
	afterN, baseN := errorCount(afterOut), errorCount(baseOut)
	if afterN <= baseN {
		return true, fmt.Sprintf("基线本就红（%d 处），本次未新增（%d 处）→ 相对基线放行", baseN, afterN)
	}
	return false, fmt.Sprintf("错误数 %d → %d，本次引入新问题：\n%s", baseN, afterN, tail(afterOut, 400))
}

func (a *App) baselineRun(ws Workspace) (int, string, bool) {
	tmp, err := os.MkdirTemp(cfg.WorktreeBase, "baseline-")
	if err != nil {
		return 0, "", false
	}
	a.gitMu.Lock()
	_, e := git(ws.Path, "worktree", "add", "--detach", tmp, ws.BaseRef)
	a.gitMu.Unlock()
	if e != nil {
		os.RemoveAll(tmp)
		return 0, "", false
	}
	defer func() {
		a.gitMu.Lock()
		git(ws.Path, "worktree", "remove", "--force", tmp)
		a.gitMu.Unlock()
		os.RemoveAll(tmp)
	}()
	if _, err := os.Stat(filepath.Join(ws.Path, "node_modules")); err == nil {
		os.Symlink(filepath.Join(ws.Path, "node_modules"), filepath.Join(tmp, "node_modules"))
	}
	rc, out := runTest(ws.TestCmd, tmp)
	return rc, out, true
}

// ── 链式处理一条记录 + tick ───────────────────────────────────────────
func (a *App) runStage(status string, rec *Record, runID string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errf("%v", r)
		}
	}()
	switch status {
	case SClarify:
		a.handleClarify(rec, runID)
	case SDev:
		a.handleDevelop(rec, runID)
	case SBug:
		a.handleBug(rec, runID)
	case SReview:
		a.handleReview(rec, runID)
	}
	return nil
}

func (a *App) processChain(rec *Record) {
	// inline 工作区共享同一棵工作树：开发/Review 必须按工作区独占。
	// 澄清若开了合批（BatchClarify），也按工作区独占——一次 agent 处理该工作区所有待澄清；
	// 没开合批则澄清只读、放开并行。
	status := fieldText(rec.Fields[FStatus])
	needLock := status != SBug && (status != SClarify || cfg.BatchClarify)
	if ws := a.workspaceFor(rec); ws.inline() && needLock {
		lk := a.wsLock(ws.Key)
		if !lk.TryLock() {
			logf("跳过「%s」· 工作区 %s 正被占用（inline 串行）", recTitle(rec), ws.Key)
			return
		}
		defer lk.Unlock()
	}
	for Actionable[fieldText(rec.Fields[FStatus])] {
		status := fieldText(rec.Fields[FStatus])
		title := recTitle(rec)
		stage := map[string]string{SClarify: "clarify", SDev: "develop", SBug: "bug", SReview: "review"}[status]
		claim := a.st.claim(rec.RecordID, stage, status, title)
		if !claim.OK {
			if claim.Reason == "retry_wait" {
				logf("跳过「%s」· 状态=%s · 等待下次重试", title, status)
			} else {
				logf("跳过「%s」· 状态=%s · 已有执行锁(%s)", title, status, claim.Reason)
			}
			return
		}
		logf("处理「%s」· 状态=%s · run=%s", title, status, claim.RunID)
		err := a.runStage(status, rec, claim.RunID)
		newStatus := fieldText(rec.Fields[FStatus])
		if err != nil {
			logf("  → 异常：%v", err)
			a.onFailure(rec, status+" 阶段异常: "+err.Error(), "")
			a.st.fail(claim.RunID, status+" 阶段异常: "+err.Error(), a.retryDelay(fieldInt(rec.Fields[FFails])))
			return
		}
		logf("  → 完成，新状态=%s", newStatus)
		if newStatus == status {
			a.st.fail(claim.RunID, status+" 阶段未推进，等待重试", a.retryDelay(fieldInt(rec.Fields[FFails])))
			return
		}
		a.st.complete(claim.RunID, status, newStatus, "stage advanced")
	}
}

// tick：扫描一轮，对每条 actionable 记录在并发上限内 fire-and-forget 处理。
// 同一记录的重复处理由 store.claim 防护；不同记录用各自 worktree，安全并行。
func (a *App) tick() {
	records, err := a.fs.listRecords()
	if err != nil {
		elog("list records 失败: %v", err)
		return
	}
	var actionable []Record
	for _, r := range records {
		if Actionable[fieldText(r.Fields[FStatus])] {
			actionable = append(actionable, r)
		}
	}
	emit("dispatcher", "tick_scanned", map[string]any{"records": len(records), "actionable": len(actionable)})
	logf("扫描 %d 条记录，待处理 %d 条", len(records), len(actionable))
	for i := range actionable {
		rec := actionable[i]
		select {
		case a.sem <- struct{}{}:
			go func(r Record) {
				defer func() { <-a.sem }()
				a.processChain(&r)
			}(rec)
		default:
			// 并发已满，留给下次 trigger/兜底轮询
		}
	}
}
