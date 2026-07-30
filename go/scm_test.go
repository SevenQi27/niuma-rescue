package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScmPrepareBugForcesWorktreeAndTracksUncommittedFiles(t *testing.T) {
	repo := t.TempDir()
	worktrees := t.TempDir()
	commands := [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "niuma-test@example.com"},
		{"config", "user.name", "niuma test"},
	}
	for _, args := range commands {
		if out, err := git(repo, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "app.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := git(repo, "add", "app.txt"); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	if out, err := git(repo, "commit", "-m", "base"); err != nil {
		t.Fatalf("git commit: %v %s", err, out)
	}

	oldCfg := cfg
	cfg = &Config{WorktreeBase: worktrees}
	t.Cleanup(func() { cfg = oldCfg })
	ws := Workspace{Key: "demo", Path: repo, SCM: "git", WorkMode: "inline", BaseRef: "main", TargetBranch: "main"}
	isolated, wt, branch, err := scmPrepareBug(ws, "rec1")
	if err != nil {
		t.Fatal(err)
	}
	if isolated.inline() || wt == repo || branch != "niuma/BUG-rec1" {
		t.Fatalf("not isolated: mode=%s wt=%s branch=%s", isolated.WorkMode, wt, branch)
	}
	if err := os.WriteFile(filepath.Join(wt, "app.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := changedFiles(isolated, wt)
	if len(files) != 1 || files[0] != "app.txt" {
		t.Fatalf("unexpected changed files: %#v", files)
	}
}
