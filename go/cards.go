package main

import (
	"fmt"
	"sort"
	"strings"
)

var templateStatus = map[string]string{
	SSetup: "turquoise", SClarify: "wathet", SAnswer: "yellow", SConfirm: "blue",
	SDevReady: "orange", SDev: "purple", SBug: "purple", SReview: "indigo", SMerge: "green", SDone: "green", SBlocked: "red",
}
var statusEmoji = map[string]string{
	SSetup: "🎛", SClarify: "🔍", SAnswer: "💬", SConfirm: "📋",
	SDevReady: "📦", SDev: "🔧", SBug: "🐛", SReview: "🔎", SMerge: "🚀", SDone: "✔️", SBlocked: "🚫",
}
var boardOrder = map[string]int{
	SBlocked: 0, SSetup: 1, SConfirm: 2, SDevReady: 3, SMerge: 4, SAnswer: 5, SClarify: 6, SDev: 7, SBug: 8, SReview: 9,
}
var agentChoices = []string{"claude", "codex", "gemini", "cursor"}

func tmpl(status, def string) string {
	if t, ok := templateStatus[status]; ok && status != "" {
		return t
	}
	return def
}

func cardHeader(title, status, template string) map[string]any {
	t := template
	if t == "" {
		t = tmpl(status, "blue")
	}
	r := []rune(title)
	if len(r) > 80 {
		title = string(r[:80])
	}
	return map[string]any{"title": map[string]any{"tag": "plain_text", "content": title}, "template": t}
}

func md(content string) map[string]any {
	return map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": content}}
}

func hr() map[string]any { return map[string]any{"tag": "hr"} }

func fieldsBlock(pairs ...[2]string) map[string]any {
	var fs []any
	for _, p := range pairs {
		v := p[1]
		if v == "" {
			v = "-"
		}
		fs = append(fs, map[string]any{"is_short": true,
			"text": map[string]any{"tag": "lark_md", "content": "**" + p[0] + "**\n" + v}})
	}
	return map[string]any{"tag": "div", "fields": fs}
}

func button(text, action, rid, btype string, extra map[string]any) map[string]any {
	if btype == "" {
		btype = "primary"
	}
	val := map[string]any{"record_id": rid, "action": action}
	for k, v := range extra {
		val[k] = v
	}
	return map[string]any{"tag": "button", "text": map[string]any{"tag": "plain_text", "content": text}, "type": btype, "value": val}
}

func actionRow(actions ...map[string]any) map[string]any {
	var a []any
	for _, x := range actions {
		a = append(a, x)
	}
	return map[string]any{"tag": "action", "actions": a}
}

func card(header map[string]any, elements ...map[string]any) map[string]any {
	var els []any
	for _, e := range elements {
		els = append(els, e)
	}
	return map[string]any{"config": map[string]any{"wide_screen_mode": true}, "header": header, "elements": els}
}

func recTitle(r *Record) string {
	if t := fieldText(r.Fields[FTitle]); t != "" {
		return t
	}
	if d := fieldText(r.Fields[FDesc]); d != "" {
		return d
	}
	return r.RecordID
}

func linkText(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "-"
	}
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		return "[打开链接](" + v + ")"
	}
	return v
}

func lastLog(r *Record) string {
	log := strings.TrimSpace(fieldText(r.Fields[FLog]))
	if log == "" {
		return ""
	}
	lines := strings.Split(log, "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func logTail(r *Record, n int) string {
	log := strings.TrimSpace(fieldText(r.Fields[FLog]))
	if log == "" {
		return ""
	}
	var lines []string
	for _, ln := range strings.Split(log, "\n") {
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, ln)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func recStatus(r *Record) string { return fieldText(r.Fields[FStatus]) }

// ── confirm / merge ──────────────────────────────────────────────────
func confirmCard(r *Record) map[string]any {
	rid := r.RecordID
	agent := fieldText(r.Fields[FAgentCode])
	if agent == "" {
		agent = fieldText(r.Fields[FAgent])
	}
	if agent == "" {
		agent = "默认"
	}
	ws := fieldText(r.Fields[FWorkspace])
	if ws == "" {
		ws = "默认"
	}
	prd := fieldText(r.Fields[FPRD])
	if prd == "" {
		prd = "（无 PRD）"
	}
	return card(cardHeader("待确认："+fieldText(r.Fields[FTitle]), SConfirm, "blue"),
		fieldsBlock([2]string{"状态", SConfirm}, [2]string{"工作区", ws}, [2]string{"开发 Agent", agent}),
		md(trunc(prd, 1800)), hr(),
		md("确认后会进入开发；想换开发 Agent / 工作区就点「⚙️ 配置」，要补充改需求直接回文字。"),
		actionRow(button("确认开发", "confirm", rid, "primary", nil),
			button("⚙️ 配置 Agent/工作区", "open_settings", rid, "default", nil)))
}

func mergeCard(r *Record) map[string]any {
	rid := r.RecordID
	ws := fieldText(r.Fields[FWorkspace])
	if ws == "" {
		ws = "默认"
	}
	agent := fieldText(r.Fields[FAgentReview])
	if agent == "" {
		agent = fieldText(r.Fields[FAgent])
	}
	if agent == "" {
		agent = "默认"
	}
	return card(cardHeader("待合并："+fieldText(r.Fields[FTitle]), SMerge, "green"),
		fieldsBlock([2]string{"状态", SMerge}, [2]string{"工作区", ws}, [2]string{"Review Agent", agent}),
		md("**Review 已通过**\n\nPR / MR / 分支：\n"+linkText(fieldText(r.Fields[FLink]))),
		md("合并或确认提交后，点击下面按钮收尾。"),
		actionRow(button("已合并 / 完成", "done", rid, "primary", nil)))
}

// devQueueCard：待开发批次队列卡。列出某工作区排队中的需求，一个按钮整批开发。
func devQueueCard(recs []*Record, wsKey string) map[string]any {
	wsLabel := orDefault(wsKey, "默认")
	var lines []string
	for i, r := range recs {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, recTitle(r)))
	}
	body := "排队中的需求会**一起**开发（合批一次执行）。攒够了点下面按钮开跑：\n\n" + strings.Join(lines, "\n")
	btn := button(fmt.Sprintf("🚀 开始开发本批（%d）", len(recs)), "start_dev_batch", "", "primary",
		map[string]any{"workspace": wsKey})
	return card(cardHeader(fmt.Sprintf("📦 待开发批次 · %s（%d）", wsLabel, len(recs)), SDevReady, ""),
		md(body),
		md("_未选进来的需求不受影响；新确认的需求会继续排进这个队列。_"),
		actionRow(btn))
}

// reviewDecisionCard：inline 开发完成后的人工决策卡——做 Review 还是直接完成。
func reviewDecisionCard(r *Record, changedCount int) map[string]any {
	rid := r.RecordID
	ws := orDefault(fieldText(r.Fields[FWorkspace]), "默认")
	agent := orDefault(fieldText(r.Fields[FAgentReview]), orDefault(fieldText(r.Fields[FAgent]), "默认"))
	return card(cardHeader("待决策："+fieldText(r.Fields[FTitle]), SMerge, "green"),
		fieldsBlock([2]string{"状态", SMerge}, [2]string{"工作区", ws}, [2]string{"Review Agent", agent}),
		md(fmt.Sprintf("**开发已完成**，改动留在工作区（共 %d 个文件，未提交）。", changedCount)),
		md("你可以让 Reviewer 先审一遍，或直接标记完成由你自己把关。"),
		actionRow(button("🔍 做 Review", "do_review", rid, "primary", nil),
			button("标记完成", "done", rid, "default", nil)))
}

// backlogCard：待处理需求池（飞书卡片 2.0）——多选勾选 + 一次确认开始。
// 选中的需求一起进入澄清，后续开发阶段自动合批。
func backlogCard(recs []*Record) map[string]any {
	var options []any
	for _, r := range recs {
		options = append(options, map[string]any{
			"text":  map[string]any{"tag": "plain_text", "content": trunc(recTitle(r), 60)},
			"value": r.RecordID,
		})
	}
	// 工作区下拉：首项「不改」表示保持现状/用默认
	wsOpts := []any{selOpt("（不改 / 用默认）", "")}
	for _, k := range workspaceKeys() {
		wsOpts = append(wsOpts, selOpt(k, k))
	}
	// Agent 下拉：首项「默认」表示不写字段、走配置默认
	agOpts := []any{selOpt("（默认）", "")}
	for _, a := range agentChoices {
		agOpts = append(agOpts, selOpt(a, a))
	}
	form := map[string]any{
		"tag": "form", "name": "backlog",
		"elements": []any{
			map[string]any{
				"tag": "multi_select_static", "name": "picked",
				"placeholder": map[string]any{"tag": "plain_text", "content": "勾选要开始的需求…"},
				"options":     options,
			},
			map[string]any{
				"tag": "select_static", "name": "workspace",
				"placeholder": map[string]any{"tag": "plain_text", "content": "工作区（去哪个仓库处理）"},
				"options":     wsOpts,
			},
			map[string]any{
				"tag": "select_static", "name": "agent",
				"placeholder": map[string]any{"tag": "plain_text", "content": "Agent（留空用默认 cursor）"},
				"options":     agOpts,
			},
			map[string]any{
				"tag": "button", "name": "go", "action_type": "form_submit",
				"text":  map[string]any{"tag": "plain_text", "content": "✅ 确认开始"},
				"type":  "primary",
				"value": map[string]any{"action": "confirm_backlog"},
			},
		},
	}
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{"update_multi": true, "wide_screen_mode": true},
		"header": map[string]any{
			"title":    map[string]any{"tag": "plain_text", "content": fmt.Sprintf("📥 待处理需求池（%d）", len(recs))},
			"template": "turquoise",
		},
		"body": map[string]any{"elements": []any{
			md2("勾选你想现在开始处理的需求，点「✅ 确认开始」。选中的会一起进入澄清流程，开发阶段自动合批成一次执行。未选的留在池子里，下次发新需求会重新列出。"),
			form,
		}},
	}
}

func backlogText(recs []*Record) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("📥 待处理需求池（%d）——回复『开始澄清』开始最新一条，或在多维表格里改状态：\n", len(recs)))
	for i, r := range recs {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, recTitle(r)))
	}
	return strings.TrimSpace(b.String())
}

// md2 是卡片 2.0 的 markdown 文本元素。
func md2(content string) map[string]any {
	return map[string]any{"tag": "markdown", "content": content}
}

// selOpt 构造 select_static / multi_select_static 的一个选项。
func selOpt(text, value string) map[string]any {
	return map[string]any{"text": map[string]any{"tag": "plain_text", "content": text}, "value": value}
}

// card2Note：简单的 2.0 提示卡，用于替换已提交的池子卡。
func card2Note(title, note, template string) map[string]any {
	if template == "" {
		template = "green"
	}
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{"update_multi": true},
		"header": map[string]any{"title": map[string]any{"tag": "plain_text", "content": trunc(title, 80)}, "template": template},
		"body":   map[string]any{"elements": []any{md2(note)}},
	}
}

func doneToastCard(title, note, template string) map[string]any {
	if template == "" {
		template = "grey"
	}
	return card(map[string]any{"title": map[string]any{"tag": "plain_text", "content": trunc(title, 80)}, "template": template}, md(note))
}

// ── progress ─────────────────────────────────────────────────────────
func progressCard(title, stage, engine string, stats map[string]any) map[string]any {
	elapsed := fieldInt(stats["elapsed"])
	used := fmt.Sprintf("%ds", elapsed)
	if elapsed >= 60 {
		used = fmt.Sprintf("%dm%02ds", elapsed/60, elapsed%60)
	}
	line := "⏱ 已用 " + used
	if _, ok := stats["tool_calls"]; ok {
		line += fmt.Sprintf("　·　🛠 %d 次工具调用", fieldInt(stats["tool_calls"]))
		if fieldInt(stats["thinking"]) > 0 {
			line += fmt.Sprintf("　·　💭 思考 %d", fieldInt(stats["thinking"]))
		}
	} else if fieldInt(stats["output_len"]) > 0 {
		line += fmt.Sprintf("　·　📝 输出 %d 字", fieldInt(stats["output_len"]))
	}
	return card(cardHeader("运行中："+title, "", "turquoise"),
		fieldsBlock([2]string{"阶段", stage}, [2]string{"Agent", engine}),
		md(line), md("_实时进度，完成后会自动更新结果。_"))
}

// ── blocked alert ────────────────────────────────────────────────────
func blockedCard(r *Record, reason string) map[string]any {
	rid := r.RecordID
	ws := fieldText(r.Fields[FWorkspace])
	if ws == "" {
		ws = "默认"
	}
	if reason == "" {
		reason = lastLog(r)
	}
	if reason == "" {
		reason = "（无）"
	}
	els := []map[string]any{
		fieldsBlock([2]string{"状态", SBlocked}, [2]string{"失败次数", fieldText(r.Fields[FFails])}, [2]string{"工作区", ws}),
		md("**阻塞原因**\n" + trunc(reason, 600)),
	}
	if t := logTail(r, 4); t != "" {
		els = append(els, md("**最近日志**\n<font color='grey'>"+trunc(t, 500)+"</font>"))
	}
	restartLabel := "重新澄清"
	if isBugRecord(r) {
		restartLabel = "重新修复"
	}
	els = append(els, actionRow(
		button("解除阻塞", "unblock_dev", rid, "primary", nil),
		button(restartLabel, "restart_clarify", rid, "default", nil),
		button("清锁", "clear_lock", rid, "default", nil)))
	return card(cardHeader("🚫 任务已阻塞："+recTitle(r), SBlocked, "red"), els...)
}

// ── status ───────────────────────────────────────────────────────────
func statusCard(r *Record) map[string]any {
	status := recStatus(r)
	rid := r.RecordID
	agentLabel := orDefault(fieldText(r.Fields[FAgent]), "默认")
	if isBugRecord(r) {
		fix, review := resolveBugAgents(r)
		agentLabel = fix + " 修复 / " + review + " Review"
	}
	els := []map[string]any{
		fieldsBlock([2]string{"状态", status}, [2]string{"工作区", orDefault(fieldText(r.Fields[FWorkspace]), "默认")},
			[2]string{"执行 Agent", agentLabel}, [2]string{"失败次数", fieldText(r.Fields[FFails])}),
		md("**链接**\n" + linkText(fieldText(r.Fields[FLink]))),
	}
	if t := logTail(r, 3); t != "" {
		els = append(els, md("**最近日志**\n<font color='grey'>"+trunc(t, 400)+"</font>"))
	}
	var acts []map[string]any
	switch {
	case Actionable[status]:
		acts = []map[string]any{button("重试", "retry", rid, "primary", nil), button("清锁", "clear_lock", rid, "default", nil)}
	case status == SBlocked:
		acts = []map[string]any{button("解除阻塞", "unblock_dev", rid, "primary", nil),
			button("重新澄清", "restart_clarify", rid, "default", nil), button("清锁", "clear_lock", rid, "default", nil)}
	case status == SConfirm:
		acts = []map[string]any{button("确认开发", "confirm", rid, "primary", nil), button("重新澄清", "restart_clarify", rid, "default", nil)}
	case status == SMerge:
		acts = []map[string]any{button("已合并 / 完成", "done", rid, "primary", nil), button("重新澄清", "restart_clarify", rid, "default", nil)}
	}
	if len(acts) > 0 {
		els = append(els, actionRow(acts...))
	}
	return card(cardHeader("当前任务："+recTitle(r), status, ""), els...)
}

// ── settings (配置 / 待选择) ──────────────────────────────────────────
func settingsHint(status string) string {
	switch {
	case status == SSetup:
		return "✅ 选好澄清 Agent 和工作区后，点「🚀 开始澄清」即用所选配置在对应工作区澄清。"
	case status == SDev || status == SBug || status == SReview || status == SMerge:
		return "⚠️ 已进入开发/Review：切换工作区**不会迁移**已建好的工作区，改 Agent 仅对之后阶段生效。"
	case status == SBlocked:
		return "提示：当前已阻塞；改完 Agent/工作区后用「解除阻塞」回到对应阶段才会生效。"
	case status == SClarify || status == SAnswer || status == SConfirm:
		return "✅ 现在设置最稳妥：开发尚未开始，选择会在后续阶段生效。"
	}
	return ""
}

func settingsCard(r *Record, wsKeys []string) map[string]any {
	rid := r.RecordID
	status := recStatus(r)
	curAgent := orDefault(fieldText(r.Fields[FAgent]), "默认")
	choices := agentChoices
	roleLabel := "执行 Agent"
	startLabel := "🚀 开始澄清"
	if isBugRecord(r) {
		fix, review := resolveBugAgents(r)
		curAgent = fix
		choices = []string{"codex", "cursor"}
		roleLabel = "修复 Agent（Review=" + review + "，自动使用另一个）"
		startLabel = "🐛 开始调查并修复"
	}
	curWS := orDefault(fieldText(r.Fields[FWorkspace]), "默认")
	var els []map[string]any
	if status == SSetup {
		els = append(els, md("👇 **选好下面的 Agent 和工作区，再点最底部开始按钮**，否则任务会一直停在这里不动。"))
	}
	els = append(els,
		fieldsBlock([2]string{"状态", status}, [2]string{"当前 Agent", curAgent}, [2]string{"当前工作区", curWS}),
		md("**"+roleLabel+"**（点按即生效）"))
	var agentBtns []map[string]any
	for _, a := range choices {
		bt := "default"
		if a == curAgent {
			bt = "primary"
		}
		agentBtns = append(agentBtns, button(a, "set_agent", rid, bt, map[string]any{"agent": a}))
	}
	els = append(els, actionRow(agentBtns...))
	if len(wsKeys) > 0 {
		els = append(els, md("**工作区**"))
		var wsBtns []map[string]any
		for i, k := range wsKeys {
			if i >= 9 {
				break
			}
			bt := "default"
			if k == curWS {
				bt = "primary"
			}
			wsBtns = append(wsBtns, button(k, "set_workspace", rid, bt, map[string]any{"workspace": k}))
		}
		els = append(els, actionRow(wsBtns...))
	}
	hint := settingsHint(status)
	if isBugRecord(r) && status == SSetup {
		hint = "✅ 开始后会强制创建独立 worktree，并使用另一个 Agent 做只读 Review。"
	}
	if h := hint; h != "" {
		els = append(els, md("<font color='grey'>"+h+"</font>"))
	}
	if status == SSetup {
		els = append(els, hr(), actionRow(button(startLabel, "start_clarify", rid, "primary", nil)))
	} else if status == SConfirm {
		els = append(els, hr(), actionRow(button("✅ 确认开发", "confirm", rid, "primary", nil)))
	}
	prefix := "配置"
	if status == SSetup {
		prefix = "新任务 · 选择执行配置"
	}
	return card(cardHeader(prefix+"："+recTitle(r), status, ""), els...)
}

// ── board ────────────────────────────────────────────────────────────
func inFlight(records []Record) []*Record {
	var fl []*Record
	for i := range records {
		if recStatus(&records[i]) != SDone {
			fl = append(fl, &records[i])
		}
	}
	sort.SliceStable(fl, func(a, b int) bool {
		oa, ok := boardOrder[recStatus(fl[a])]
		if !ok {
			oa = 9
		}
		ob, ok := boardOrder[recStatus(fl[b])]
		if !ok {
			ob = 9
		}
		return oa < ob
	})
	return fl
}

func boardActions(status, rid string) []map[string]any {
	switch status {
	case SBlocked:
		return []map[string]any{button("解除阻塞", "unblock_dev", rid, "primary", nil),
			button("重试", "retry", rid, "default", nil), button("清锁", "clear_lock", rid, "default", nil)}
	case SConfirm:
		return []map[string]any{button("确认开发", "confirm", rid, "primary", nil)}
	case SMerge:
		return []map[string]any{button("已合并 / 完成", "done", rid, "primary", nil)}
	}
	return nil
}

func boardText(records []Record) string {
	fl := inFlight(records)
	if len(fl) == 0 {
		return "任务看板：当前没有进行中的任务。"
	}
	lines := []string{fmt.Sprintf("任务看板 · %d 条在途：", len(fl))}
	for _, r := range fl {
		st := recStatus(r)
		mark := ""
		if n := fieldInt(r.Fields[FFails]); n > 0 {
			mark = fmt.Sprintf("（失败%d）", n)
		}
		lines = append(lines, fmt.Sprintf("· %s %s%s | %s", statusEmoji[st], st, mark, recTitle(r)))
	}
	return strings.Join(lines, "\n")
}

func boardCard(records []Record) map[string]any {
	fl := inFlight(records)
	if len(fl) == 0 {
		return card(cardHeader("任务看板 · 无在途任务", "", "grey"),
			md("当前没有进行中的任务。发「需求@cursor：…」或「Bug@codex：…」开一条。"))
	}
	counts := map[string]int{}
	for _, r := range fl {
		counts[recStatus(r)]++
	}
	var parts []string
	for _, st := range []string{SBlocked, SSetup, SConfirm, SMerge, SAnswer, SClarify, SDev, SBug, SReview} {
		if counts[st] > 0 {
			parts = append(parts, fmt.Sprintf("%s%s %d", statusEmoji[st], st, counts[st]))
		}
	}
	els := []map[string]any{md(strings.Join(parts, "　·　")), hr()}
	for _, r := range fl {
		st := recStatus(r)
		mark := ""
		if n := fieldInt(r.Fields[FFails]); n > 0 {
			mark = fmt.Sprintf("　·　❌ 失败 %d", n)
		}
		row := fmt.Sprintf("%s **%s** — %s%s", statusEmoji[st], trunc(recTitle(r), 60), st, mark)
		if l := lastLog(r); l != "" {
			row += "\n<font color='grey'>" + trunc(l, 120) + "</font>"
		}
		els = append(els, md(row))
		if acts := boardActions(st, r.RecordID); acts != nil {
			els = append(els, actionRow(acts...))
		}
		els = append(els, hr())
	}
	if len(els) > 0 && els[len(els)-1]["tag"] == "hr" {
		els = els[:len(els)-1]
	}
	return card(cardHeader(fmt.Sprintf("任务看板 · %d 条在途", len(fl)), "", "blue"), els...)
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
