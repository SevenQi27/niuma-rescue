package main

import (
	"os"
	"path/filepath"
	"strings"
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

func TestScmPrepareBugUsesLatestOriginMainWithoutUpstream(t *testing.T) {
	repo := t.TempDir()
	remote := filepath.Join(t.TempDir(), "origin.git")
	worktrees := filepath.Join(t.TempDir(), "qtcc-worktrees")
	rulesSource := t.TempDir()
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rulesSource, ".claude", "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rulesSource, "AGENTS.md"), []byte("shared rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rulesSource, ".claude", "rules", "git-workflow.md"), []byte("no main upstream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, command := range []struct {
		dir  string
		args []string
	}{
		{remote, []string{"init", "--bare", "-b", "main"}},
		{repo, []string{"init", "-b", "main"}},
		{repo, []string{"config", "user.email", "niuma-test@example.com"}},
		{repo, []string{"config", "user.name", "niuma test"}},
		{repo, []string{"remote", "add", "origin", remote}},
	} {
		if out, err := git(command.dir, command.args...); err != nil {
			t.Fatalf("git %v: %v %s", command.args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "app.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := git(repo, "add", "app.txt"); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	if out, err := git(repo, "commit", "-m", "first"); err != nil {
		t.Fatalf("git commit: %v %s", err, out)
	}
	first, _ := git(repo, "rev-parse", "HEAD")
	if out, err := git(repo, "push", "origin", "main"); err != nil {
		t.Fatalf("git push first: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "app.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := git(repo, "commit", "-am", "second"); err != nil {
		t.Fatalf("git commit second: %v %s", err, out)
	}
	latest, _ := git(repo, "rev-parse", "HEAD")
	if out, err := git(repo, "push", "origin", "main"); err != nil {
		t.Fatalf("git push second: %v %s", err, out)
	}
	if out, err := git(repo, "reset", "--hard", strings.TrimSpace(first)); err != nil {
		t.Fatalf("git reset test fixture: %v %s", err, out)
	}
	if out, err := git(repo, "update-ref", "refs/remotes/origin/main", strings.TrimSpace(first)); err != nil {
		t.Fatalf("make remote ref stale: %v %s", err, out)
	}

	oldCfg := cfg
	cfg = &Config{WorktreeBase: t.TempDir()}
	t.Cleanup(func() { cfg = oldCfg })
	ws := Workspace{
		Key: "qtcc", Path: repo, SCM: "git", WorkMode: "worktree",
		WorktreeBase: worktrees, RulesSource: rulesSource, BaseRef: "origin/main", TargetBranch: "main",
	}
	_, wt, branch, err := scmPrepareBug(ws, "rec-latest")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(wt) != worktrees {
		t.Fatalf("unexpected worktree root: %s", wt)
	}
	head, _ := git(wt, "rev-parse", "HEAD")
	if strings.TrimSpace(head) != strings.TrimSpace(latest) {
		t.Fatalf("branch did not start at latest origin/main: got=%s want=%s", strings.TrimSpace(head), strings.TrimSpace(latest))
	}
	if target, err := os.Readlink(filepath.Join(wt, "AGENTS.md")); err != nil || target != filepath.Join(rulesSource, "AGENTS.md") {
		t.Fatalf("AGENTS.md rules link: target=%q err=%v", target, err)
	}
	if target, err := os.Readlink(filepath.Join(wt, ".claude", "rules")); err != nil || target != filepath.Join(rulesSource, ".claude", "rules") {
		t.Fatalf(".claude rules link: target=%q err=%v", target, err)
	}
	upstream, err := git(repo, "for-each-ref", "--format=%(upstream:short)", "refs/heads/"+branch)
	if err != nil || strings.TrimSpace(upstream) != "" {
		t.Fatalf("branch must not track main: upstream=%q err=%v", strings.TrimSpace(upstream), err)
	}
}

func TestVerifyBugMergedRequiresTargetBranchEvidence(t *testing.T) {
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
	_, wt, branch, err := scmPrepareBug(ws, "rec2")
	if err != nil {
		t.Fatal(err)
	}
	if branch != bugBranchName("rec2") {
		t.Fatalf("unexpected branch: %s", branch)
	}
	if err := os.WriteFile(filepath.Join(wt, "app.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := git(wt, "add", "app.txt"); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	if out, err := git(wt, "commit", "-m", "fix"); err != nil {
		t.Fatalf("git commit: %v %s", err, out)
	}
	if ok, _ := verifyBugMerged(ws, "rec2"); ok {
		t.Fatal("unmerged bug branch must not be accepted")
	}
	if out, err := git(repo, "merge", "--ff-only", branch); err != nil {
		t.Fatalf("git merge: %v %s", err, out)
	}
	if ok, detail := verifyBugMerged(ws, "rec2"); !ok {
		t.Fatalf("merged bug branch rejected: %s", detail)
	}
}

func TestGitCommitExcludesPipelineDossier(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "niuma-test@example.com"},
		{"config", "user.name", "niuma test"},
	} {
		if out, err := git(repo, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "app.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := git(repo, "add", "app.txt"); err != nil {
		t.Fatal(out)
	}
	if out, err := git(repo, "commit", "-m", "base"); err != nil {
		t.Fatal(out)
	}
	if err := os.WriteFile(filepath.Join(repo, "app.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dossier := filepath.Join(repo, dossierDir, "BUG-rec", "images")
	if err := os.MkdirAll(dossier, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dossier, "screen.png"), []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := gitCommitAllChecked(repo, "fix"); err != nil {
		t.Fatal(err)
	}
	tracked, err := git(repo, "ls-files", dossierDir)
	if err != nil || strings.TrimSpace(tracked) != "" {
		t.Fatalf("pipeline dossier must not be committed: %q err=%v", tracked, err)
	}
}
