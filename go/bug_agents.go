package main

func validBugAgent(engine string) bool {
	e := normalizeAgent(engine)
	return e == "codex" || e == "cursor"
}

// bugAgentPair 保证修复与 Review 使用两个不同的 Agent。
func bugAgentPair(requestedFix, defaultFix, defaultReview string) (string, string) {
	fix := normalizeAgent(requestedFix)
	if !validBugAgent(fix) {
		fix = normalizeAgent(defaultFix)
	}
	if !validBugAgent(fix) {
		fix = "codex"
	}
	review := normalizeAgent(defaultReview)
	if !validBugAgent(review) || review == fix || validBugAgent(requestedFix) {
		if fix == "codex" {
			review = "cursor"
		} else {
			review = "codex"
		}
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
