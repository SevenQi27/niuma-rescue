package main

import (
	"regexp"
	"strings"
)

var intakeRe = regexp.MustCompile(`^(需求|(?i:bug)|缺陷)(?:\s*@([A-Za-z][\w -]*))?\s*[：:]?\s*([\s\S]*)$`)
var spaceRe = regexp.MustCompile(`\s+`)

var commandHelp = `可用指令：
看板（所有在途需求一览）
需求池（待选择需求，勾选确认开始）
开始开发（把「待开发」队列整批开跑）
状态（当前会话的需求）
配置（点按选择 Agent / 工作区）
健康（服务在不在跑 / 有无卡住）
统计 / 周报（运行报表）
重试 / 清锁 / 解除阻塞 / 重新澄清 / 完成
切换Agent <claude|codex|gemini|cursor>
切换工作区 <workspace>
设置状态 <状态>

新任务格式：
需求：修改登录页按钮样式
Bug@claude：登录接口偶发 500
  发完会先停在「待选择」，选好 Agent/工作区点开始按钮才开跑（PIPELINE_SETUP_GATE=0 可关）。`

var activeStatuses = map[string]bool{
	SSetup: true, SClarify: true, SAnswer: true, SConfirm: true,
	SDevReady: true, SDev: true, SBug: true, SCodeWait: true, SReview: true, SMerge: true, SBlocked: true,
}

func parseIntake(text string) (body, agent, wsKey, taskType string, ok bool) {
	m := intakeRe.FindStringSubmatch(text)
	if m == nil {
		return "", "", "", "", false
	}
	body = strings.Trim(strings.TrimSpace(m[3]), "：: ")
	body, wsKey = parseWorkspaceToken(body)
	body = strings.Trim(strings.TrimSpace(body), "：: ")
	agent = normalizeAgent(m[2])
	taskType = TaskRequirement
	if strings.EqualFold(m[1], "bug") || m[1] == "缺陷" {
		taskType = TaskBug
	}
	return body, agent, wsKey, taskType, true
}

func isBugRecord(rec *Record) bool {
	return rec != nil && strings.EqualFold(fieldText(rec.Fields[FTaskType]), TaskBug)
}

func startStatus(rec *Record) string {
	if isBugRecord(rec) {
		return SBug
	}
	return SClarify
}

func findActive(records []Record, chatID string) *Record {
	var found *Record
	for i := range records {
		r := &records[i]
		if fieldText(r.Fields[FChat]) == chatID && activeStatuses[fieldText(r.Fields[FStatus])] {
			found = r // 取最后一条（最新）
		}
	}
	return found
}

func findRecordStatus(records []Record, chatID string, statuses map[string]bool) *Record {
	for i := range records {
		r := &records[i]
		if fieldText(r.Fields[FChat]) == chatID && statuses[fieldText(r.Fields[FStatus])] {
			return r
		}
	}
	return nil
}

func findByID(records []Record, rid string) *Record {
	for i := range records {
		if records[i].RecordID == rid {
			return &records[i]
		}
	}
	return nil
}

func (a *App) sendCardOrText(chatID string, c map[string]any, fallback string) {
	if _, err := a.fs.sendCard(chatID, c); err != nil {
		elog("发卡片失败，回退文本 chat=%s: %v", chatID, err)
		a.fs.sendText(chatID, fallback)
	}
}

func recordDiag(r *Record) string {
	f := r.Fields
	clar := orDefault(fieldText(f[FAgentClarify]), orDefault(fieldText(f[FAgent]), cfg.EngineClarify))
	code := orDefault(fieldText(f[FAgentCode]), orDefault(fieldText(f[FAgent]), cfg.EngineCode))
	rev := orDefault(fieldText(f[FAgentReview]), orDefault(fieldText(f[FAgent]), cfg.EngineReview))
	return strings.Join([]string{
		"需求诊断：" + recTitle(r),
		"· record_id: " + r.RecordID,
		"· 状态: " + orDefault(fieldText(f[FStatus]), "-"),
		"· 失败次数: " + fieldText(f[FFails]),
		"· 工作区: " + orDefault(fieldText(f[FWorkspace]), "默认"),
		"· Agent: 澄清=" + clar + " / 开发=" + code + " / Review=" + rev,
		"· 最近日志: " + orDefault(lastLog(r), "-"),
	}, "\n")
}

// handleCommand 返回 (matched, dispatch)。matched=false 表示不是命令，交给后续 intake。
func (a *App) handleCommand(text, chatID string, records []Record) (bool, bool) {
	n := strings.TrimSpace(spaceRe.ReplaceAllString(text, " "))
	switch n {
	case "指令", "帮助", "help", "/help":
		a.fs.sendText(chatID, commandHelp)
		return true, false
	case "看板", "全部", "board", "/board":
		a.sendCardOrText(chatID, boardCard(records), boardText(records))
		return true, false
	case "需求池", "池子", "backlog", "/pool":
		var pool []*Record
		for i := range records {
			r := &records[i]
			if fieldText(r.Fields[FChat]) == chatID && fieldText(r.Fields[FStatus]) == SSetup {
				pool = append(pool, r)
			}
		}
		if len(pool) == 0 {
			a.fs.sendText(chatID, "📥 需求池是空的。发「需求：<描述>」往里加。")
			return true, false
		}
		a.sendCardOrText(chatID, backlogCard(pool), backlogText(pool))
		return true, false
	case "配置", "config", "/config":
		rec := findActive(records, chatID)
		if rec == nil {
			a.fs.sendText(chatID, "当前会话没有进行中的需求。")
			return true, false
		}
		a.sendCardOrText(chatID, settingsCard(rec, workspaceKeys()), "回复『切换Agent cursor』『切换工作区 <key>』来设置。")
		return true, false
	case "健康", "系统", "health":
		a.fs.sendText(chatID, systemHealthText(a.st))
		return true, false
	case "统计", "报表", "stats":
		a.fs.sendText(chatID, summaryText(24))
		return true, false
	case "周报", "weekly":
		a.fs.sendText(chatID, summaryText(168))
		return true, false
	case "诊断", "diagnose":
		rec := findActive(records, chatID)
		if rec == nil {
			a.fs.sendText(chatID, "当前会话没有进行中的需求。")
			return true, false
		}
		a.fs.sendText(chatID, recordDiag(rec))
		return true, false
	case "状态":
		rec := findActive(records, chatID)
		if rec == nil {
			a.fs.sendText(chatID, "当前会话没有进行中的需求。")
			return true, false
		}
		a.sendCardOrText(chatID, statusCard(rec), "状态："+fieldText(rec.Fields[FStatus]))
		return true, false
	case "开始澄清", "开始修复", "开始", "go":
		rec := findRecordStatus(records, chatID, map[string]bool{SSetup: true})
		if rec == nil {
			return false, false // 不在待选择语境 → 交给后续
		}
		next := startStatus(rec)
		a.fs.updateRecord(rec.RecordID, map[string]any{FStatus: next})
		if next == SBug {
			a.fs.sendText(chatID, "🐛 开始调查并修复 Bug。")
		} else {
			a.fs.sendText(chatID, "🚀 开始澄清。")
		}
		return true, true
	case "开始开发", "开跑":
		rec := findRecordStatus(records, chatID, map[string]bool{SDevReady: true})
		if rec == nil {
			a.fs.sendText(chatID, "当前没有「待开发」的需求。")
			return true, false
		}
		toast, dispatch, _ := a.startDevBatch(map[string]any{"workspace": a.workspaceFor(rec).Key}, records)
		a.fs.sendText(chatID, toast)
		return true, dispatch
	}

	// 带参数命令
	rec := findActive(records, chatID)
	var res *opResult
	switch {
	case n == "重试":
		if rec == nil {
			break
		}
		r := a.retryRecord(rec)
		res = &r
	case n == "清锁":
		if rec == nil {
			break
		}
		r := a.clearLock(rec)
		res = &r
	case n == "重新澄清":
		if rec == nil {
			break
		}
		r := a.restartClarify(rec)
		res = &r
	case n == "完成" || n == "标记完成":
		if rec == nil {
			break
		}
		r := a.markDone(rec)
		res = &r
	case strings.HasPrefix(n, "解除阻塞"):
		if rec == nil {
			break
		}
		target := SDev
		if isBugRecord(rec) {
			target = SBug
		}
		if f := strings.Fields(n); len(f) == 2 {
			target = f[1]
		}
		r := a.unblockRecord(rec, target)
		res = &r
	case strings.HasPrefix(n, "切换工作区 ") || strings.HasPrefix(n, "设置工作区 "):
		if rec == nil {
			break
		}
		r := a.setWorkspace(rec, strings.TrimSpace(n[len("切换工作区 "):]))
		res = &r
	case agentCmdRe.MatchString(n):
		if rec == nil {
			break
		}
		m := agentCmdRe.FindStringSubmatch(n)
		stage := map[string]string{"澄清": "clarify", "开发": "code", "Review": "review", "review": "review"}[m[1]]
		r := a.setAgent(rec, m[2], stage)
		res = &r
	case statusCmdRe.MatchString(n):
		if rec == nil {
			break
		}
		m := statusCmdRe.FindStringSubmatch(n)
		r := a.setStatus(rec, m[1])
		res = &r
	default:
		return false, false // 不是命令
	}
	if rec == nil {
		a.fs.sendText(chatID, "当前会话没有可操作的需求。")
		return true, false
	}
	if res == nil {
		return false, false
	}
	prefix := "✅"
	if !res.ok {
		prefix = "⚠️"
	}
	a.fs.sendText(chatID, prefix+" "+res.msg)
	return true, res.dispatch
}

var agentCmdRe = regexp.MustCompile(`^(?:切换|设置)(澄清|开发|Review|review)?Agent (.+)$`)
var statusCmdRe = regexp.MustCompile(`^设置状态 (待选择|待澄清|待回答|待确认|待开发|开发中|Bug处理中|Review中|待合并|完成|已阻塞)$`)

func (a *App) appendClarify(rec *Record, answer string) {
	merged := strings.TrimSpace(fieldText(rec.Fields[FClarify]) + "\n\n【回答】" + answer)
	next := SClarify
	if isBugRecord(rec) {
		next = SBug
	}
	a.fs.updateRecord(rec.RecordID, map[string]any{FClarify: merged, FStatus: next})
	rec.Fields[FClarify] = merged
	rec.Fields[FStatus] = next
}

// handleMessage 处理一条飞书文本消息。返回 true 表示进入"机器该处理"的状态，需触发 dispatch。
func (a *App) handleMessage(msg map[string]any) bool {
	if fieldText(msg["message_type"]) != "text" {
		return false
	}
	chatID := fieldText(msg["chat_id"])
	text := strings.TrimSpace(fieldText(msg["content"]))
	sender := fieldText(msg["sender_id"])
	if chatID == "" || text == "" {
		return false
	}
	records, err := a.fs.listRecords()
	if err != nil {
		elog("list records: %v", err)
		return false
	}
	if matched, dispatch := a.handleCommand(text, chatID, records); matched {
		return dispatch
	}
	// 人工卡点输入
	if rec := findRecordStatus(records, chatID, HumanInput); rec != nil {
		status := fieldText(rec.Fields[FStatus])
		if status == SAnswer {
			a.appendClarify(rec, text)
			a.fs.sendText(chatID, "👌 收到，我再看看还需不需要补充。")
			return true
		}
		if status == SConfirm {
			low := strings.ToLower(text)
			confirmed := false
			for _, w := range ConfirmWords {
				if strings.Contains(low, strings.ToLower(w)) {
					confirmed = true
					break
				}
			}
			if confirmed {
				a.fs.updateRecord(rec.RecordID, map[string]any{FStatus: SDevReady})
				a.fs.sendText(chatID, "👌 已确认，进入「待开发」队列。攒齐要做的几条后，点批次卡片的「开始开发本批」（或回复『开始开发』）整批开跑。")
				a.sendDevQueueCard(chatID, a.workspaceFor(rec))
			} else {
				a.appendClarify(rec, text)
				a.fs.sendText(chatID, "已记录你的补充，我再过一遍。")
			}
			return true
		}
		return false
	}
	// 新需求
	body, agent, wsKey, taskType, ok := parseIntake(text)
	if !ok {
		a.fs.sendText(chatID, "发「需求：<描述>」或「Bug@claude：<现象>」给我，就能提交任务开始走流水线。")
		return false
	}
	// 幂等：同会话+同描述已有在途记录 → 跳过
	for _, r := range records {
		if fieldText(r.Fields[FChat]) == chatID && fieldText(r.Fields[FDesc]) == body &&
			orDefault(fieldText(r.Fields[FTaskType]), TaskRequirement) == taskType {
			st := fieldText(r.Fields[FStatus])
			if st != SDone && st != SBlocked {
				return false
			}
		}
	}
	a.fs.sendText(chatID, "✅ 收到需求，正在准备…")
	gated := cfg.SetupGate
	status := SClarify
	if taskType == TaskBug {
		status = SBug
	}
	if gated {
		status = SSetup
	}
	fields := map[string]any{FTitle: trunc(body, 30), FDesc: body, FStatus: status, FChat: chatID, FTaskType: taskType}
	if sender != "" {
		fields[FOwner] = []map[string]string{{"id": sender}}
	}
	if wsKey != "" {
		fields[FWorkspace] = wsKey
	}
	if taskType == TaskBug {
		fix, review := bugAgentPair(agent, cfg.EngineBugFix, cfg.EngineBugReview)
		fields[FAgentCode] = fix
		fields[FAgentReview] = review
	} else if agent != "" {
		fields[FAgent] = agent
	}
	created, err := a.fs.createRecord(fields)
	if err != nil {
		a.fs.sendText(chatID, "建记录失败："+err.Error())
		return false
	}
	if gated {
		// 只记录，不开跑。把本会话所有「待选择」需求列成需求池卡片，由人勾选确认。
		pool := []*Record{created}
		for i := range records {
			r := &records[i]
			if r.RecordID != created.RecordID && fieldText(r.Fields[FChat]) == chatID && fieldText(r.Fields[FStatus]) == SSetup {
				pool = append(pool, r)
			}
		}
		a.fs.sendText(chatID, "✅ 已记录到需求池。")
		a.sendCardOrText(chatID, backlogCard(pool), backlogText(pool))
		return false
	}
	if taskType == TaskBug {
		a.fs.sendText(chatID, "🐛 已收到，正在创建隔离工作树并调查 Bug…")
	} else {
		a.fs.sendText(chatID, "🔍 已收到，正在澄清需求…")
	}
	return true
}

// confirmBacklog 处理需求池的多选提交：把勾选的「待选择」需求一起推进到「待澄清」。
func (a *App) confirmBacklog(value map[string]any, records []Record) (string, bool, map[string]any) {
	fv, _ := value["_form"].(map[string]any)
	picked := toStrList(fv["picked"])
	if len(picked) == 0 {
		return "没有勾选任何需求", false, nil
	}
	// 可选：把表单里选的工作区 / Agent 一并写到选中的需求上
	wsKey := fieldText(fv["workspace"])
	if wsKey != "" {
		if _, err := workspaceGet(wsKey); err != nil {
			return "工作区 `" + wsKey + "` 无效：" + err.Error(), false, nil
		}
	}
	agent := ""
	if a := normalizeAgent(fieldText(fv["agent"])); a != "" {
		if _, ok := AgentCmds[a]; ok {
			agent = a
		}
	}
	n := 0
	var titles []string
	for _, id := range picked {
		r := findByID(records, id)
		if r == nil || fieldText(r.Fields[FStatus]) != SSetup {
			continue
		}
		fields := map[string]any{FStatus: startStatus(r)}
		if wsKey != "" {
			fields[FWorkspace] = wsKey
		}
		if isBugRecord(r) {
			fix, review := bugAgentPair(agent, cfg.EngineBugFix, cfg.EngineBugReview)
			fields[FAgentCode] = fix
			fields[FAgentReview] = review
		} else if agent != "" {
			fields[FAgent] = agent
		}
		if err := a.fs.updateRecord(id, fields); err == nil {
			n++
			titles = append(titles, recTitle(r))
		}
	}
	if n == 0 {
		return "勾选的需求都不在「待选择」状态了", false, nil
	}
	extra := ""
	if wsKey != "" {
		extra += " · 工作区=" + wsKey
	}
	if agent != "" {
		extra += " · Agent=" + agent
	}
	note := "已进入处理流程" + extra + "，完成后会同步进度。未选的仍留在任务池。\n\n本批：" + strings.Join(titles, " / ")
	return "🚀 已开始 " + itoa(n) + " 个需求", true, card2Note("🚀 已开始 "+itoa(n)+" 个需求", note, "green")
}

// sendDevQueueCard 把某工作区在本会话里所有「待开发」需求列成批次队列卡片。
func (a *App) sendDevQueueCard(chatID string, ws Workspace) {
	records, err := a.fs.listRecords()
	if err != nil {
		return
	}
	var q []*Record
	for i := range records {
		r := &records[i]
		if fieldText(r.Fields[FStatus]) == SDevReady && fieldText(r.Fields[FChat]) == chatID && a.workspaceFor(r).Key == ws.Key {
			q = append(q, r)
		}
	}
	if len(q) == 0 {
		return
	}
	a.sendCardOrText(chatID, devQueueCard(q, ws.Key),
		"📦 "+ws.Key+" 待开发队列有 "+itoa(len(q))+" 条，回复『开始开发』整批开始。")
}

// startDevBatch 把某工作区所有「待开发」需求整批推进到「开发中」，由 dispatcher 合批执行。
func (a *App) startDevBatch(value map[string]any, records []Record) (string, bool, map[string]any) {
	wsKey := fieldText(value["workspace"])
	var titles []string
	for i := range records {
		r := &records[i]
		if fieldText(r.Fields[FStatus]) != SDevReady || a.workspaceFor(r).Key != wsKey {
			continue
		}
		if err := a.fs.updateRecord(r.RecordID, map[string]any{FStatus: SDev}); err == nil {
			titles = append(titles, recTitle(r))
		}
	}
	if len(titles) == 0 {
		return "该工作区没有待开发的需求了", false, nil
	}
	note := "已整批进入开发（合批一次执行）：\n" + strings.Join(titles, " / ") + "\n完成后会逐条推「待合并」决策卡。"
	return "🚀 开始开发 " + itoa(len(titles)) + " 个需求", true,
		doneToastCard("🚀 开始开发本批（"+itoa(len(titles))+"）", note, "purple")
}

// handleCardAction 处理卡片按钮。返回 (toast, dispatch, 替换卡片或 nil)。
func (a *App) handleCardAction(value map[string]any) (string, bool, map[string]any) {
	action := fieldText(value["action"])
	records, err := a.fs.listRecords()
	if err != nil {
		return "查询失败", false, nil
	}
	// 需求池多选提交：没有单条 record_id，单独处理。
	// 兜底：部分 form_submit 不回传按钮 value，只要带了表单值就按需求池提交处理。
	if action == "confirm_backlog" || (action == "" && value["_form"] != nil) {
		return a.confirmBacklog(value, records)
	}
	// 待开发批次触发：按工作区把所有「待开发」整批推进到「开发中」。
	if action == "start_dev_batch" {
		return a.startDevBatch(value, records)
	}
	rid := fieldText(value["record_id"])
	if rid == "" {
		return "无效操作", false, nil
	}
	rec := findByID(records, rid)
	if rec == nil {
		return "记录不存在或已删除", false, nil
	}
	status := fieldText(rec.Fields[FStatus])
	title := fieldText(rec.Fields[FTitle])
	switch action {
	case "confirm":
		if status != SConfirm {
			return "当前状态「" + status + "」，无需确认", false, nil
		}
		a.fs.updateRecord(rid, map[string]any{FStatus: SDevReady})
		a.sendDevQueueCard(fieldText(rec.Fields[FChat]), a.workspaceFor(rec))
		return "✅ 已确认，进入待开发队列", false, doneToastCard("✅ 已确认："+title, "已进入「待开发」队列；攒齐后点批次卡片的「🚀 开始开发本批」整批开跑。", "blue")
	case "done":
		r := a.markDone(rec)
		if !r.ok {
			return r.msg, false, doneToastCard("⚠️ 尚未完成："+title, r.msg, "yellow")
		}
		return "✅ 已确认合并并完成", false, doneToastCard("✅ 已完成："+title, r.msg, "green")
	case "do_review":
		if status != SMerge {
			return "当前状态「" + status + "」，无法发起 Review", false, nil
		}
		if isBugRecord(rec) {
			return "Bug 已经过独立 Agent Review；请人工检查并合并，无需重复走需求 Review。", false, nil
		}
		a.fs.updateRecord(rid, map[string]any{FStatus: SReview})
		agent := orDefault(fieldText(rec.Fields[FAgentReview]), orDefault(fieldText(rec.Fields[FAgent]), cfg.EngineReview))
		return "🔍 已发起 Review", true, doneToastCard("🔍 开始 Review："+title, "将用 "+agent+" 审查当前工作区改动，请稍候。", "wathet")
	case "start_clarify":
		if status != SSetup {
			return "当前状态「" + status + "」，无需开始澄清", false, nil
		}
		next := startStatus(rec)
		a.fs.updateRecord(rid, map[string]any{FStatus: next})
		ws := orDefault(fieldText(rec.Fields[FWorkspace]), "默认")
		if next == SBug {
			fix, review := bugAgentPair(fieldText(rec.Fields[FAgentCode]), cfg.EngineBugFix, cfg.EngineBugReview)
			return "🐛 开始修复", true, doneToastCard("🐛 开始修复："+title, "将用 "+fix+" 修复、"+review+" Review，并在工作区 "+ws+" 创建隔离 worktree。", "purple")
		}
		agent := orDefault(fieldText(rec.Fields[FAgent]), cfg.EngineClarify)
		return "🚀 开始澄清", true, doneToastCard("🚀 开始澄清："+title, "将用 "+agent+" 在工作区 "+ws+" 澄清，请稍候。", "wathet")
	case "open_settings":
		return "打开配置", false, settingsCard(rec, workspaceKeys())
	case "set_agent":
		r := a.setAgent(rec, fieldText(value["agent"]), "")
		return r.msg, false, settingsCard(rec, workspaceKeys())
	case "set_workspace":
		r := a.setWorkspace(rec, fieldText(value["workspace"]))
		if !r.ok {
			return r.msg, false, nil
		}
		return r.msg, false, settingsCard(rec, workspaceKeys())
	}
	// 恢复类
	ops := map[string]func(*Record) opResult{
		"retry": a.retryRecord, "clear_lock": a.clearLock, "restart_clarify": a.restartClarify,
		"unblock_dev": func(r *Record) opResult {
			target := SDev
			if isBugRecord(r) {
				target = SBug
			}
			return a.unblockRecord(r, target)
		}, "mark_done": a.markDone,
	}
	if fn, ok := ops[action]; ok {
		r := fn(rec)
		tmpl := "green"
		prefix := "✅ "
		if !r.ok {
			tmpl = "yellow"
			prefix = "⚠️ "
		}
		return r.msg, r.dispatch, doneToastCard(prefix+title, r.msg, tmpl)
	}
	return "未知操作", false, nil
}
