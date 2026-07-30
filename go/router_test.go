package main

import "testing"

func TestParseIntakeRequirement(t *testing.T) {
	body, agent, ws, taskType, ok := parseIntake("需求@codex：修复登录页 #web")
	if !ok || body != "修复登录页" || agent != "codex" || ws != "web" || taskType != TaskRequirement {
		t.Fatalf("unexpected parse result: ok=%v body=%q agent=%q ws=%q type=%q", ok, body, agent, ws, taskType)
	}
}

func TestParseIntakeBug(t *testing.T) {
	body, agent, ws, taskType, ok := parseIntake("Bug@cursor: 登录接口偶发 500 #backend")
	if !ok || body != "登录接口偶发 500" || agent != "cursor" || ws != "backend" || taskType != TaskBug {
		t.Fatalf("unexpected parse result: ok=%v body=%q agent=%q ws=%q type=%q", ok, body, agent, ws, taskType)
	}
}

func TestBugAgentPairAlwaysDistinct(t *testing.T) {
	tests := []struct {
		requested, wantFix, wantReview string
	}{
		{"", "codex", "cursor"},
		{"cursor", "cursor", "codex"},
		{"codex", "codex", "cursor"},
		{"gemini", "codex", "cursor"},
	}
	for _, tt := range tests {
		fix, review := bugAgentPair(tt.requested, "codex", "cursor")
		if fix != tt.wantFix || review != tt.wantReview || fix == review {
			t.Fatalf("requested=%q got %s/%s want %s/%s", tt.requested, fix, review, tt.wantFix, tt.wantReview)
		}
	}
}

func TestReplaceEnvRemovesDuplicate(t *testing.T) {
	got := replaceEnv([]string{"PATH=/bin", "PYTHONPATH=/old", "PYTHONPATH=/older"}, "PYTHONPATH", "/new")
	want := []string{"PATH=/bin", "PYTHONPATH=/new"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %#v want %#v", got, want)
	}
}
