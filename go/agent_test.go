package main

import (
	"reflect"
	"regexp"
	"testing"
)

func TestAgentArgvResumesExactCodexSession(t *testing.T) {
	sessionID := "019fdb28-2a22-7ea2-80a5-b20b2464cf28"
	want := []string{"codex", "exec", "resume", sessionID, "-"}
	if got := agentArgv("codex", sessionID); !reflect.DeepEqual(got, want) {
		t.Fatalf("agentArgv() = %#v, want %#v", got, want)
	}
}

func TestAgentArgvLeavesOtherEnginesUnchanged(t *testing.T) {
	want := AgentCmds["cursor"]
	if got := agentArgv("cursor", "ignored-session"); !reflect.DeepEqual(got, want) {
		t.Fatalf("agentArgv() = %#v, want %#v", got, want)
	}
}

func TestAgentArgvResumesExactClaudeSession(t *testing.T) {
	sessionID := "019fdb28-2a22-7ea2-80a5-b20b2464cf28"
	want := append(append([]string{}, AgentCmds["claude"]...), "--resume", sessionID)
	if got := agentArgv("claude", sessionID); !reflect.DeepEqual(got, want) {
		t.Fatalf("agentArgv() = %#v, want %#v", got, want)
	}
}

func TestAgentArgvEnablesClaudeEditsOnlyForWritePhase(t *testing.T) {
	sessionID := "019fdb28-2a22-7ea2-80a5-b20b2464cf28"
	want := append(append([]string{}, AgentCmds["claude"]...),
		"--permission-mode", "acceptEdits", "--resume", sessionID)
	if got := agentArgvWithAccess("claude", sessionID, true); !reflect.DeepEqual(got, want) {
		t.Fatalf("agentArgvWithAccess() = %#v, want %#v", got, want)
	}
	if got := agentArgvWithAccess("claude", sessionID, false); reflect.DeepEqual(got, want) {
		t.Fatalf("read-only claude invocation unexpectedly enables edits: %#v", got)
	}
	if got := agentArgvWithAccess("codex", sessionID, true); !reflect.DeepEqual(got, []string{"codex", "exec", "resume", sessionID, "-"}) {
		t.Fatalf("non-Claude argv changed: %#v", got)
	}
}

func TestNewAgentSessionIDReturnsUUID(t *testing.T) {
	got := newAgentSessionID()
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(got) {
		t.Fatalf("newAgentSessionID() = %q", got)
	}
}

func TestExtractCodexSessionID(t *testing.T) {
	want := "019fdb28-2a22-7ea2-80a5-b20b2464cf28"
	lines := []string{"OpenAI Codex", "session id: " + want, "--------"}
	if got := extractCodexSessionID(lines); got != want {
		t.Fatalf("extractCodexSessionID() = %q, want %q", got, want)
	}
}
