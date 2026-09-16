package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateReadonlySQLAcceptsQueries(t *testing.T) {
	queries := []string{
		"SELECT * FROM ibps_bpm_tasks WHERE proc_inst_id = '123' LIMIT 10",
		"SHOW GRANTS FOR CURRENT_USER()",
		"DESCRIBE `ibps_bpm_tasks`",
		"EXPLAIN SELECT * FROM ibps_bpm_tasks",
		"WITH active AS (SELECT id FROM tasks) SELECT * FROM active",
	}
	for _, query := range queries {
		if _, err := validateReadonlySQL(query); err != nil {
			t.Errorf("query should be accepted: %q: %v", query, err)
		}
	}
}

func TestValidateReadonlySQLRejectsWritesAndEscapes(t *testing.T) {
	queries := []string{
		"UPDATE ibps_bpm_tasks SET status = 'done'",
		"WITH changed AS (DELETE FROM tasks RETURNING id) SELECT * FROM changed",
		"SELECT * FROM tasks FOR UPDATE",
		"SELECT * INTO OUTFILE '/tmp/tasks' FROM tasks",
		"SELECT GET_LOCK('niuma', 10)",
		"SELECT 1; DELETE FROM tasks",
		"SELECT 1 -- hide a write",
	}
	for _, query := range queries {
		if _, err := validateReadonlySQL(query); err == nil {
			t.Errorf("query should be rejected: %q", query)
		}
	}
}

func TestReadonlyMySQLMCPAdvertisesOnlyReadTools(t *testing.T) {
	for _, tool := range readonlyMySQLTools() {
		if tool.Annotations["readOnlyHint"] != true || tool.Annotations["destructiveHint"] != false {
			t.Fatalf("unsafe annotations for %s: %#v", tool.Name, tool.Annotations)
		}
	}
}

func TestInquiryCodexPreapprovesOnlyReadonlyProductionWrapper(t *testing.T) {
	joined := strings.Join(agentArgvWithAccess(codexInquiryEngine, "", false), " ")
	for _, expected := range []string{"mysql-readonly-mcp", "mysql-prod.default_tools_approval_mode=\"approve\"", "--sandbox read-only"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("inquiry command missing %q: %s", expected, joined)
		}
	}
	if strings.Contains(joined, "dangerously-bypass") {
		t.Fatalf("inquiry command bypasses protections: %s", joined)
	}
}

func TestResolveMySQLClientUsesExplicitExecutable(t *testing.T) {
	dir := t.TempDir()
	client := filepath.Join(dir, "mysql")
	if err := os.WriteFile(client, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MYSQL_CLIENT_PATH", client)
	resolved, err := resolveMySQLClient()
	if err != nil {
		t.Fatal(err)
	}
	if resolved != client {
		t.Fatalf("resolved=%q want=%q", resolved, client)
	}
}
