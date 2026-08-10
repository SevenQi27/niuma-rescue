package main

import "testing"

func TestMarkDoneTrustsManualBugConfirmation(t *testing.T) {
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.db.Close() })

	records := newHybridRecords(newLocalRecords(st), t.TempDir(), integrationSettings{})
	rec, err := records.createRecord(map[string]any{
		FTitle:    "尚未合入 main 的 Bug",
		FTaskType: TaskBug,
		FStatus:   SMerge,
	})
	if err != nil {
		t.Fatal(err)
	}

	app := &App{fs: records, records: records, st: st}
	result := app.markDone(rec)
	if !result.ok {
		t.Fatalf("markDone() rejected manual confirmation: %s", result.msg)
	}
	if got := fieldText(rec.Fields[FStatus]); got != SDone {
		t.Fatalf("status=%q, want %q", got, SDone)
	}
	if got := fieldText(rec.Fields[FLog]); got != "[manual] 已确认人工合并并完成" {
		t.Fatalf("log=%q", got)
	}
}

func TestMarkDoneStillRequiresMergeStatus(t *testing.T) {
	rec := &Record{RecordID: "rec-developing", Fields: map[string]any{
		FTitle:    "仍在修复中的 Bug",
		FTaskType: TaskBug,
		FStatus:   SBug,
	}}

	result := (&App{}).markDone(rec)
	if result.ok {
		t.Fatal("markDone() accepted a task that is not waiting for merge")
	}
	if got := fieldText(rec.Fields[FStatus]); got != SBug {
		t.Fatalf("status=%q, want unchanged %q", got, SBug)
	}
}
