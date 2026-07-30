package main

import "strings"

type opResult struct {
	msg      string
	ok       bool
	dispatch bool
}

func (a *App) updateWithLog(rec *Record, fields map[string]any, logLine string) {
	if logLine != "" {
		prev := fieldText(rec.Fields[FLog])
		fields[FLog] = strings.TrimSpace(prev+logLine+"\n") + "\n"
	}
	a.fs.updateRecord(rec.RecordID, fields)
	for k, v := range fields {
		rec.Fields[k] = v
	}
}

func (a *App) retryRecord(rec *Record) opResult {
	status := fieldText(rec.Fields[FStatus])
	title := recTitle(rec)
	switch status {
	case SDone:
		return opResult{"「" + title + "」已完成，不能重试。", false, false}
	case SMerge:
		return opResult{"「" + title + "」已到待合并；如需重做请先重新澄清或改回开发中。", false, false}
	case SBlocked:
		return opResult{"「" + title + "」已阻塞，请先解除阻塞。", false, false}
	}
	a.st.clear(rec.RecordID, "manual retry")
	a.updateWithLog(rec, map[string]any{FFails: 0}, "[manual] 清锁/重试，准备立即重试")
	return opResult{"已安排「" + title + "」立即重试。", true, Actionable[status]}
}

func (a *App) clearLock(rec *Record) opResult {
	cleared := a.st.clear(rec.RecordID, "manual clear lock")
	suffix := "已清理"
	if !cleared {
		suffix = "没有本地锁需要清理"
	}
	a.updateWithLog(rec, map[string]any{}, "[manual] "+suffix)
	return opResult{suffix + "：" + recTitle(rec), true, false}
}

func (a *App) unblockRecord(rec *Record, status string) opResult {
	if !Actionable[status] {
		return opResult{"解除阻塞目标状态必须是：待澄清 / 开发中 / Bug处理中 / Review中", false, false}
	}
	a.st.clear(rec.RecordID, "manual unblock")
	a.updateWithLog(rec, map[string]any{FStatus: status, FFails: 0}, "[manual] 解除阻塞，回到"+status)
	return opResult{"已解除阻塞并回到「" + status + "」：" + recTitle(rec), true, true}
}

func (a *App) markDone(rec *Record) opResult {
	a.st.clear(rec.RecordID, "manual done")
	a.updateWithLog(rec, map[string]any{FStatus: SDone}, "[manual] 人工标记完成")
	return opResult{"已标记完成：" + recTitle(rec), true, false}
}

func (a *App) restartClarify(rec *Record) opResult {
	if fieldText(rec.Fields[FStatus]) == SDone {
		return opResult{"「" + recTitle(rec) + "」已完成，不能重新澄清。", false, false}
	}
	next := SClarify
	label := "澄清"
	if isBugRecord(rec) {
		next = SBug
		label = "Bug 修复"
	}
	a.st.clear(rec.RecordID, "manual restart "+label)
	a.updateWithLog(rec, map[string]any{FStatus: next, FFails: 0}, "[manual] 重新进入"+label)
	return opResult{"已重新进入" + label + "：" + recTitle(rec), true, true}
}

func (a *App) setAgent(rec *Record, agent, stage string) opResult {
	e := normalizeAgent(agent)
	if _, ok := AgentCmds[e]; !ok {
		return opResult{"未知 Agent：" + agent, false, false}
	}
	if isBugRecord(rec) {
		if !validBugAgent(e) {
			return opResult{"Bug 流水线当前只支持 codex / cursor，且修复与 Review 必须不同。", false, false}
		}
		fix, review := resolveBugAgents(rec)
		if stage == "review" {
			review = e
			if review == fix {
				if review == "codex" {
					fix = "cursor"
				} else {
					fix = "codex"
				}
			}
		} else {
			fix, review = bugAgentPair(e, cfg.EngineBugFix, cfg.EngineBugReview)
		}
		fields := map[string]any{FAgentCode: fix, FAgentReview: review}
		a.updateWithLog(rec, fields, "[manual] 设置 Bug Agent：修复="+fix+" Review="+review)
		return opResult{"已设置 Bug Agent：修复=" + fix + " / Review=" + review + "：" + recTitle(rec), true, false}
	}
	field := map[string]string{"clarify": FAgentClarify, "code": FAgentCode, "review": FAgentReview}[stage]
	if field == "" {
		field = FAgent
	}
	a.updateWithLog(rec, map[string]any{field: e}, "[manual] 设置"+field+"="+e)
	return opResult{"已设置 " + field + "=" + e + "：" + recTitle(rec), true, false}
}

func (a *App) setWorkspace(rec *Record, key string) opResult {
	if _, err := workspaceGet(key); err != nil {
		return opResult{"工作区无效：" + err.Error(), false, false}
	}
	a.updateWithLog(rec, map[string]any{FWorkspace: key}, "[manual] 设置工作区="+key)
	return opResult{"已设置工作区=" + key + "：" + recTitle(rec), true, false}
}

func (a *App) setStatus(rec *Record, status string) opResult {
	a.st.clear(rec.RecordID, "manual set status "+status)
	a.updateWithLog(rec, map[string]any{FStatus: status}, "[manual] 设置状态="+status)
	return opResult{"已设置状态=" + status + "：" + recTitle(rec), true, Actionable[status]}
}
