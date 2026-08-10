package main

import "testing"

func TestParseIntakeRequirement(t *testing.T) {
	body, agent, ws, taskType, ok := parseIntake("需求@codex：修复登录页 #web")
	if !ok || body != "修复登录页" || agent != "codex" || ws != "web" || taskType != TaskRequirement {
		t.Fatalf("unexpected parse result: ok=%v body=%q agent=%q ws=%q type=%q", ok, body, agent, ws, taskType)
	}
}

func TestParseIntakeBug(t *testing.T) {
	body, agent, ws, taskType, ok := parseIntake("Bug@claude: 登录接口偶发 500 #backend")
	if !ok || body != "登录接口偶发 500" || agent != "claude" || ws != "backend" || taskType != TaskBug {
		t.Fatalf("unexpected parse result: ok=%v body=%q agent=%q ws=%q type=%q", ok, body, agent, ws, taskType)
	}
}

func TestBugAgentPairAlwaysDistinct(t *testing.T) {
	tests := []struct {
		requested, wantFix, wantReview string
	}{
		{"", "claude", "codex"},
		{"claude", "claude", "codex"},
		{"cursor", "cursor", "codex"},
		{"codex", "codex", "claude"},
		{"gemini", "claude", "codex"},
	}
	for _, tt := range tests {
		fix, review := bugAgentPair(tt.requested, "claude", "codex")
		if fix != tt.wantFix || review != tt.wantReview || fix == review {
			t.Fatalf("requested=%q got %s/%s want %s/%s", tt.requested, fix, review, tt.wantFix, tt.wantReview)
		}
	}
}

func TestValidatePipelineSettingsAllowsClaudeAndCodex(t *testing.T) {
	settings := pipelineSettings{
		BugFixAgent: "claude", BugReviewAgent: "codex", BugRepairLimit: 2,
		TimeoutCode: 1800, TimeoutReview: 900, TimeoutBug: 3600,
	}
	if err := validatePipelineSettings(settings); err != nil {
		t.Fatalf("validatePipelineSettings() rejected Claude/Codex: %v", err)
	}
	settings.BugReviewAgent = "claude"
	if err := validatePipelineSettings(settings); err == nil {
		t.Fatal("validatePipelineSettings() accepted identical fix/review agents")
	}
}

func TestReplaceEnvRemovesDuplicate(t *testing.T) {
	got := replaceEnv([]string{"PATH=/bin", "PYTHONPATH=/old", "PYTHONPATH=/older"}, "PYTHONPATH", "/new")
	want := []string{"PATH=/bin", "PYTHONPATH=/new"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %#v want %#v", got, want)
	}
}
