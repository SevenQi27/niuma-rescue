package main

func validBugAgent(engine string) bool {
	e := normalizeAgent(engine)
	return e == "claude" || e == "codex" || e == "cursor"
}

func bugAgentExcept(excluded string, candidates ...string) string {
	excluded = normalizeAgent(excluded)
	for _, candidate := range candidates {
		agent := normalizeAgent(candidate)
		if validBugAgent(agent) && agent != excluded {
			return agent
		}
	}
	return ""
}

// bugAgentPair 保证修复与 Review 使用两个不同的 Agent。
func bugAgentPair(requestedFix, defaultFix, defaultReview string) (string, string) {
	fix := normalizeAgent(requestedFix)
	if !validBugAgent(fix) {
		fix = normalizeAgent(defaultFix)
	}
	if !validBugAgent(fix) {
		fix = "claude"
	}
	review := normalizeAgent(defaultReview)
	if !validBugAgent(review) || review == fix {
		review = bugAgentExcept(fix, defaultReview, "codex", "claude", "cursor")
	}
	return fix, review
}

func resolveBugAgents(rec *Record) (string, string) {
	fix := fieldText(rec.Fields[FAgentCode])
	review := normalizeAgent(fieldText(rec.Fields[FAgentReview]))
	resolvedFix, resolvedReview := bugAgentPair(fix, cfg.EngineBugFix, cfg.EngineBugReview)
	if validBugAgent(review) && review != resolvedFix {
		resolvedReview = review
	}
	return resolvedFix, resolvedReview
}
