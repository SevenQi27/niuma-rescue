package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newSessionTestApp(t *testing.T) (*App, Workspace) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "niuma-test@example.com"},
		{"config", "user.name", "niuma test"},
	} {
		if out, err := git(repo, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "app.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := git(repo, "add", "app.txt"); err != nil {
		t.Fatal(out)
	}
	if out, err := git(repo, "commit", "-m", "base"); err != nil {
		t.Fatal(out)
	}
	store, err := openStore(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.db.Close() })
	oldCfg := cfg
	cfg = &Config{WorktreeBase: filepath.Join(root, "worktrees")}
	t.Cleanup(func() { cfg = oldCfg })
	ws := Workspace{
		Key: "demo", Path: repo, SCM: "git", WorkMode: "worktree",
		WorkspaceScope: "session", QueueMode: "parallel", SessionRollover: "manual",
		DeliveryTarget: "user_choose", BaseRef: "main", TargetBranch: "main",
	}
	return &App{st: store}, ws
}

func TestDevelopmentSessionRunsTasksInParallelAndIntegratesSerially(t *testing.T) {
	app, ws := newSessionTestApp(t)
	first, err := app.prepareCodeTask(ws, "BUG", "A")
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.prepareCodeTask(ws, "REQ", "B")
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionID == "" || first.SessionID != second.SessionID {
		t.Fatalf("tasks did not share one session: %#v %#v", first, second)
	}
	if first.Worktree == second.Worktree || first.Branch == second.Branch {
		t.Fatal("parallel tasks must use different worktrees and branches")
	}
	if first.CompareRef != second.CompareRef {
		t.Fatalf("tasks should start from the same session snapshot: %s != %s", first.CompareRef, second.CompareRef)
	}
	if err := os.WriteFile(filepath.Join(first.Worktree, "a.txt"), []byte("A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second.Worktree, "b.txt"), []byte("B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := app.commitDevelopmentTask(first, "A", "task A"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.commitDevelopmentTask(second, "B", "task B"); err != nil {
		t.Fatal(err)
	}
	session, err := app.integrateDevelopmentTask(first, "A")
	if err != nil {
		t.Fatal(err)
	}
	session, err = app.integrateDevelopmentTask(second, "B")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(session.Worktree, name)); err != nil {
			t.Fatalf("session missing %s: %v", name, err)
		}
	}
	upstream, err := git(ws.Path, "for-each-ref", "--format=%(upstream:short)", "refs/heads/"+session.Branch)
	if err != nil || strings.TrimSpace(upstream) != "" {
		t.Fatalf("session branch must not track upstream: %q %v", upstream, err)
	}
	if err := app.freezeDevelopmentSession(session.SessionID); err != nil {
		t.Fatal(err)
	}
	frozen, ok := app.st.developmentSession(session.SessionID)
	if !ok || frozen.State != "frozen" {
		t.Fatalf("session not frozen: %#v", frozen)
	}
}

func TestDevelopmentSessionConflictOnlyBlocksConflictingTask(t *testing.T) {
	app, ws := newSessionTestApp(t)
	first, err := app.prepareCodeTask(ws, "BUG", "A")
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.prepareCodeTask(ws, "BUG", "B")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Worktree, "app.txt"), []byte("from A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second.Worktree, "app.txt"), []byte("from B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := app.commitDevelopmentTask(first, "A", "task A"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.commitDevelopmentTask(second, "B", "task B"); err != nil {
		t.Fatal(err)
	}
	session, err := app.integrateDevelopmentTask(first, "A")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.integrateDevelopmentTask(second, "B"); err == nil || !strings.Contains(err.Error(), "仅暂停当前任务") {
		t.Fatalf("expected isolated integration conflict, got %v", err)
	}
	body, err := os.ReadFile(filepath.Join(session.Worktree, "app.txt"))
	if err != nil || string(body) != "from A\n" {
		t.Fatalf("session branch was polluted by conflict: %q %v", body, err)
	}
	if files := productWorkingTreeChanges(session.Worktree); len(files) != 0 {
		t.Fatalf("session worktree must stay clean: %#v", files)
	}
	conflicted, ok := app.st.developmentTask("B")
	if !ok || conflicted.State != "integration_conflict" {
		t.Fatalf("conflicting task state=%#v", conflicted)
	}
}
