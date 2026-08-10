package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceToolEnvOverridesJavaAndMaven(t *testing.T) {
	ws := Workspace{JavaHome: "/tools/jdk8", MavenHome: "/tools/maven"}
	env := workspaceToolEnv([]string{"PATH=/usr/bin", "JAVA_HOME=/old"}, ws)

	if got := envValue(env, "JAVA_HOME"); got != "/tools/jdk8" {
		t.Fatalf("JAVA_HOME=%q", got)
	}
	if got := envValue(env, "MAVEN_HOME"); got != "/tools/maven" {
		t.Fatalf("MAVEN_HOME=%q", got)
	}
	if got := envValue(env, "M2_HOME"); got != "/tools/maven" {
		t.Fatalf("M2_HOME=%q", got)
	}
	wantPrefix := strings.Join([]string{"/tools/jdk8/bin", "/tools/maven/bin", ""}, string(os.PathListSeparator))
	if got := envValue(env, "PATH"); !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("PATH=%q, want prefix %q", got, wantPrefix)
	}
}

func TestValidateWorkspaceToolHome(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	java := filepath.Join(bin, "java")
	if err := os.WriteFile(java, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateWorkspaceToolHome("demo", "java_home", home, filepath.Join("bin", "java")); err != nil {
		t.Fatal(err)
	}
	if err := validateWorkspaceToolHome("demo", "maven_home", home, filepath.Join("bin", "mvn")); err == nil {
		t.Fatal("missing bin/mvn must be rejected")
	}
}

func TestWorkspaceToolEnvForConfiguredWorktree(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	worktrees := filepath.Join(root, "worktrees")
	worktree := filepath.Join(worktrees, "BUG-1")
	for _, dir := range []string{repo, worktree} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(root, "workspaces.json")
	content := `{"default":"demo","items":{"demo":{"path":"` + repo + `","worktree_base":"` + worktrees + `","java_home":"/jdk8","maven_home":"/maven"}}}`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	oldCfg, oldCache := cfg, wsCache
	cfg = &Config{WorkspacesFile: configPath, WorktreeBase: filepath.Join(root, "global-worktrees")}
	wsMu.Lock()
	wsCache = nil
	wsMu.Unlock()
	t.Cleanup(func() {
		cfg = oldCfg
		wsMu.Lock()
		wsCache = oldCache
		wsMu.Unlock()
	})

	env := workspaceToolEnvForDir([]string{"PATH=/usr/bin"}, worktree)
	if got := envValue(env, "JAVA_HOME"); got != "/jdk8" {
		t.Fatalf("JAVA_HOME=%q", got)
	}
	if got := envValue(env, "MAVEN_HOME"); got != "/maven" {
		t.Fatalf("MAVEN_HOME=%q", got)
	}
}
