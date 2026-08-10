package main

import "testing"

func TestBugFileClaimsWaitThenStackAfterReview(t *testing.T) {
	store, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.db.Close()

	first := bugCoordinationRow{
		RecordID: "BUG-A", Workspace: "qtcc", Branch: "niuma/BUG-A", State: "diagnosed",
		Diagnosis: "same method", Files: []string{"src/SaleService.java"},
	}
	if err := store.saveBugCoordination(first); err != nil {
		t.Fatal(err)
	}
	decision, err := store.claimBugFiles(first)
	if err != nil || !decision.Allowed {
		t.Fatalf("first claim=%#v err=%v", decision, err)
	}

	second := bugCoordinationRow{
		RecordID: "BUG-B", Workspace: "qtcc", Branch: "niuma/BUG-B", State: "diagnosed",
		Diagnosis: "another symptom", Files: []string{"src/SaleService.java"},
	}
	if err := store.saveBugCoordination(second); err != nil {
		t.Fatal(err)
	}
	decision, err = store.claimBugFiles(second)
	if err != nil || decision.Allowed || decision.PredecessorID != "BUG-A" || decision.WaitReason == "" {
		t.Fatalf("second must wait: decision=%#v err=%v", decision, err)
	}

	store.markBugCoordination("BUG-A", "reviewed")
	decision, err = store.claimBugFiles(second)
	if err != nil || decision.Allowed || decision.PredecessorID != "BUG-A" || decision.WaitReason != "" {
		t.Fatalf("reviewed predecessor must be stackable: decision=%#v err=%v", decision, err)
	}
	if err := store.takeBugFiles(second, "BUG-A"); err != nil {
		t.Fatal(err)
	}
	row, ok := store.bugCoordination("BUG-B")
	if !ok || row.State != "editing" || row.PredecessorID != "BUG-A" {
		t.Fatalf("unexpected coordination row: %#v ok=%v", row, ok)
	}
	store.markBugCoordination("BUG-B", "failed")
	third := bugCoordinationRow{
		RecordID: "BUG-C", Workspace: "qtcc", Branch: "niuma/BUG-C", State: "diagnosed",
		Files: []string{"src/SaleService.java"},
	}
	if err := store.saveBugCoordination(third); err != nil {
		t.Fatal(err)
	}
	decision, err = store.claimBugFiles(third)
	if err != nil || decision.PredecessorID != "BUG-A" || decision.WaitReason != "" {
		t.Fatalf("failed stacked task must restore reviewed predecessor: decision=%#v err=%v", decision, err)
	}
}

func TestUnknownBugScopeConflictsWithKnownFiles(t *testing.T) {
	store, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.db.Close()
	unknown := bugCoordinationRow{RecordID: "BUG-A", Workspace: "qtcc", Branch: "a", Files: nil}
	known := bugCoordinationRow{RecordID: "BUG-B", Workspace: "qtcc", Branch: "b", Files: []string{"src/App.java"}}
	if err := store.saveBugCoordination(unknown); err != nil {
		t.Fatal(err)
	}
	if decision, err := store.claimBugFiles(unknown); err != nil || !decision.Allowed {
		t.Fatalf("unknown claim=%#v err=%v", decision, err)
	}
	if err := store.saveBugCoordination(known); err != nil {
		t.Fatal(err)
	}
	if decision, err := store.claimBugFiles(known); err != nil || decision.PredecessorID != "BUG-A" {
		t.Fatalf("known scope must conflict with wildcard: %#v err=%v", decision, err)
	}
}

func TestBugSimilarityUsesChineseShingles(t *testing.T) {
	left := &Record{RecordID: "a", Fields: map[string]any{FTitle: "订单保存时报空指针", FDesc: "提交订单后页面报错"}}
	right := &Record{RecordID: "b", Fields: map[string]any{FTitle: "保存订单出现空指针", FDesc: "订单提交以后页面报错"}}
	unrelated := &Record{RecordID: "c", Fields: map[string]any{FTitle: "导出销售报表", FDesc: "增加 Excel 下载"}}
	if bugSimilarity(left, right) <= bugSimilarity(left, unrelated) {
		t.Fatalf("similar Bug should receive a higher score")
	}
}
