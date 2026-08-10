package main

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode"
)

const unknownBugScope = "*"

type bugCoordinationRow struct {
	RecordID      string
	Workspace     string
	Branch        string
	State         string
	Diagnosis     string
	Files         []string
	SessionID     string
	PredecessorID string
	SimilarRecord string
	Similarity    float64
}

type bugFileClaim struct {
	RecordID string
	Branch   string
	State    string
	File     string
}

type bugClaimDecision struct {
	Allowed       bool
	PredecessorID string
	Branch        string
	WaitReason    string
}

type webBugCoordination struct {
	State         string   `json:"state,omitempty"`
	AffectedFiles []string `json:"affected_files,omitempty"`
	PredecessorID string   `json:"predecessor_id,omitempty"`
	SimilarTaskID string   `json:"similar_task_id,omitempty"`
	Similarity    float64  `json:"similarity,omitempty"`
	MergeOrder    []string `json:"merge_order,omitempty"`
	Message       string   `json:"message,omitempty"`
}

func normalizeBugFiles(files []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(files))
	for _, file := range files {
		file = strings.TrimSpace(strings.TrimPrefix(strings.ReplaceAll(file, "\\", "/"), "./"))
		if file == "" || strings.HasPrefix(file, "/") || strings.Contains(file, "../") {
			continue
		}
		if !seen[file] {
			seen[file] = true
			out = append(out, file)
		}
	}
	if len(out) == 0 {
		return []string{unknownBugScope}
	}
	sort.Strings(out)
	return out
}

func (s *Store) saveBugCoordination(row bugCoordinationRow) error {
	row.Files = normalizeBugFiles(row.Files)
	body, _ := json.Marshal(row.Files)
	_, err := s.db.Exec(`INSERT INTO bug_coordination
		(record_id,workspace,branch,state,diagnosis,files_json,session_id,predecessor_id,similar_record_id,similarity,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(record_id) DO UPDATE SET workspace=excluded.workspace,branch=excluded.branch,
		state=excluded.state,diagnosis=excluded.diagnosis,files_json=excluded.files_json,
		session_id=excluded.session_id,similar_record_id=excluded.similar_record_id,
		similarity=excluded.similarity,updated_at=excluded.updated_at`,
		row.RecordID, row.Workspace, row.Branch, row.State, row.Diagnosis, string(body), row.SessionID,
		row.PredecessorID, row.SimilarRecord, row.Similarity, nowf())
	return err
}

func (s *Store) bugCoordination(recordID string) (bugCoordinationRow, bool) {
	var row bugCoordinationRow
	var filesJSON string
	err := s.db.QueryRow(`SELECT record_id,workspace,branch,state,COALESCE(diagnosis,''),files_json,
		COALESCE(session_id,''),COALESCE(predecessor_id,''),COALESCE(similar_record_id,''),similarity
		FROM bug_coordination WHERE record_id=?`, recordID).Scan(
		&row.RecordID, &row.Workspace, &row.Branch, &row.State, &row.Diagnosis, &filesJSON,
		&row.SessionID, &row.PredecessorID, &row.SimilarRecord, &row.Similarity)
	if err != nil {
		return bugCoordinationRow{}, false
	}
	_ = json.Unmarshal([]byte(filesJSON), &row.Files)
	row.Files = normalizeBugFiles(row.Files)
	return row, true
}

func bugFilesOverlap(left, right []string) bool {
	for _, a := range left {
		for _, b := range right {
			if a == unknownBugScope || b == unknownBugScope || a == b {
				return true
			}
		}
	}
	return false
}

func (s *Store) claimBugFiles(row bugCoordinationRow) (bugClaimDecision, error) {
	row.Files = normalizeBugFiles(row.Files)
	tx, err := s.db.Begin()
	if err != nil {
		return bugClaimDecision{}, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT record_id,branch,state,file_path FROM bug_code_claims
		WHERE workspace=? AND record_id<>? AND state IN ('editing','reviewed')`, row.Workspace, row.RecordID)
	if err != nil {
		return bugClaimDecision{}, err
	}
	var conflicts []bugFileClaim
	for rows.Next() {
		var claim bugFileClaim
		if rows.Scan(&claim.RecordID, &claim.Branch, &claim.State, &claim.File) == nil && bugFilesOverlap(row.Files, []string{claim.File}) {
			conflicts = append(conflicts, claim)
		}
	}
	rows.Close()
	if len(conflicts) > 0 {
		sort.SliceStable(conflicts, func(i, j int) bool {
			if conflicts[i].State != conflicts[j].State {
				return conflicts[i].State == "editing"
			}
			return conflicts[i].RecordID < conflicts[j].RecordID
		})
		owner := conflicts[0]
		state := "waiting"
		_, err = tx.Exec(`UPDATE bug_coordination SET state=?,predecessor_id=?,updated_at=? WHERE record_id=?`,
			state, owner.RecordID, nowf(), row.RecordID)
		if err != nil {
			return bugClaimDecision{}, err
		}
		if err := tx.Commit(); err != nil {
			return bugClaimDecision{}, err
		}
		if owner.State == "reviewed" {
			return bugClaimDecision{PredecessorID: owner.RecordID, Branch: owner.Branch}, nil
		}
		return bugClaimDecision{
			PredecessorID: owner.RecordID,
			Branch:        owner.Branch,
			WaitReason:    "代码范围正由 " + owner.RecordID + " 修改，等待其 Review 通过",
		}, nil
	}
	for _, file := range row.Files {
		if _, err = tx.Exec(`INSERT INTO bug_code_claims(workspace,file_path,record_id,branch,state,updated_at)
			VALUES (?,?,?,?,'editing',?) ON CONFLICT(workspace,file_path) DO UPDATE SET
			record_id=excluded.record_id,branch=excluded.branch,state='editing',updated_at=excluded.updated_at`,
			row.Workspace, file, row.RecordID, row.Branch, nowf()); err != nil {
			return bugClaimDecision{}, err
		}
	}
	if _, err = tx.Exec(`UPDATE bug_coordination SET state='editing',predecessor_id='',updated_at=? WHERE record_id=?`, nowf(), row.RecordID); err != nil {
		return bugClaimDecision{}, err
	}
	if err := tx.Commit(); err != nil {
		return bugClaimDecision{}, err
	}
	return bugClaimDecision{Allowed: true}, nil
}

func (s *Store) takeBugFiles(row bugCoordinationRow, predecessorID string) error {
	row.Files = normalizeBugFiles(row.Files)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, file := range row.Files {
		if file == unknownBugScope {
			if _, err = tx.Exec(`DELETE FROM bug_code_claims WHERE workspace=? AND record_id=?`, row.Workspace, predecessorID); err != nil {
				return err
			}
		} else if _, err = tx.Exec(`DELETE FROM bug_code_claims WHERE workspace=? AND record_id=? AND (file_path=? OR file_path=?)`,
			row.Workspace, predecessorID, file, unknownBugScope); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO bug_code_claims(workspace,file_path,record_id,branch,state,updated_at)
			VALUES (?,?,?,?,'editing',?) ON CONFLICT(workspace,file_path) DO UPDATE SET
			record_id=excluded.record_id,branch=excluded.branch,state='editing',updated_at=excluded.updated_at`,
			row.Workspace, file, row.RecordID, row.Branch, nowf()); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`UPDATE bug_coordination SET state='editing',predecessor_id=?,updated_at=? WHERE record_id=?`,
		predecessorID, nowf(), row.RecordID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) markBugCoordination(recordID, state string) {
	_, _ = s.db.Exec(`UPDATE bug_coordination SET state=?,updated_at=? WHERE record_id=?`, state, nowf(), recordID)
	claimState := state
	if state != "editing" && state != "reviewed" {
		row, ok := s.bugCoordination(recordID)
		_, _ = s.db.Exec(`DELETE FROM bug_code_claims WHERE record_id=?`, recordID)
		if ok && row.PredecessorID != "" {
			predecessor, predecessorOK := s.bugCoordination(row.PredecessorID)
			if predecessorOK && predecessor.State == "reviewed" {
				for _, file := range normalizeBugFiles(row.Files) {
					_, _ = s.db.Exec(`INSERT INTO bug_code_claims(workspace,file_path,record_id,branch,state,updated_at)
						VALUES (?,?,?,?,'reviewed',?) ON CONFLICT(workspace,file_path) DO NOTHING`,
						row.Workspace, file, predecessor.RecordID, predecessor.Branch, nowf())
				}
			}
		}
		return
	}
	_, _ = s.db.Exec(`UPDATE bug_code_claims SET state=?,updated_at=? WHERE record_id=?`, claimState, nowf(), recordID)
}

func (s *Store) bugDependents(predecessorID string) []string {
	rows, err := s.db.Query(`SELECT record_id FROM bug_coordination WHERE predecessor_id=? AND state='waiting' ORDER BY updated_at`, predecessorID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *Store) resetBugDependency(recordID string) {
	_, _ = s.db.Exec(`UPDATE bug_coordination SET state='diagnosed',predecessor_id='',updated_at=? WHERE record_id=?`, nowf(), recordID)
}

func (s *Store) readyBugDependency(recordID string) {
	_, _ = s.db.Exec(`UPDATE bug_coordination SET state='ready',updated_at=? WHERE record_id=?`, nowf(), recordID)
}

func bugSimilarity(left, right *Record) float64 {
	return shingleSimilarity(recTitle(left)+" "+fieldText(left.Fields[FDesc]), recTitle(right)+" "+fieldText(right.Fields[FDesc]))
}

func shingleSimilarity(left, right string) float64 {
	shingles := func(value string) map[string]bool {
		value = strings.ToLower(value)
		var cleaned []rune
		for _, r := range value {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				cleaned = append(cleaned, r)
			}
		}
		out := map[string]bool{}
		if len(cleaned) == 1 {
			out[string(cleaned)] = true
		}
		for i := 0; i+1 < len(cleaned); i++ {
			out[string(cleaned[i:i+2])] = true
		}
		return out
	}
	a, b := shingles(left), shingles(right)
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	intersection := 0
	for item := range a {
		if b[item] {
			intersection++
		}
	}
	return float64(intersection) / float64(len(a)+len(b)-intersection)
}

func mostSimilarActiveBug(rec *Record, records []Record, workspace string) (string, float64) {
	bestID, best := "", 0.0
	for i := range records {
		other := &records[i]
		if other.RecordID == rec.RecordID || !isBugRecord(other) || fieldText(other.Fields[FWorkspace]) != workspace {
			continue
		}
		status := fieldText(other.Fields[FStatus])
		if status != SBug && status != SCodeWait && status != SReview && status != SMerge {
			continue
		}
		if score := bugSimilarity(rec, other); score > best {
			bestID, best = other.RecordID, score
		}
	}
	if best < 0.35 {
		return "", 0
	}
	return bestID, best
}

func (a *App) bugSimilarityCandidate(rec *Record, ws Workspace) (string, float64) {
	records, err := a.fs.listRecords()
	if err != nil {
		return "", 0
	}
	bestID, best := "", 0.0
	for i := range records {
		other := &records[i]
		if other.RecordID == rec.RecordID || !isBugRecord(other) || a.workspaceFor(other).Key != ws.Key {
			continue
		}
		status := fieldText(other.Fields[FStatus])
		if status != SBug && status != SCodeWait && status != SReview && status != SMerge {
			continue
		}
		if score := bugSimilarity(rec, other); score > best {
			bestID, best = other.RecordID, score
		}
	}
	if best < 0.35 {
		return "", 0
	}
	return bestID, best
}

func (a *App) wakeBugDependents(predecessorID string, reviewed bool) {
	ids := a.st.bugDependents(predecessorID)
	if len(ids) == 0 {
		return
	}
	records, err := a.fs.listRecords()
	if err != nil {
		return
	}
	for _, id := range ids {
		rec := findByID(records, id)
		if rec == nil || fieldText(rec.Fields[FStatus]) != SCodeWait {
			continue
		}
		message := "[coordination] 前置任务 " + predecessorID + " Review 已通过，按依赖顺序继续修复"
		if !reviewed {
			a.st.resetBugDependency(id)
			message = "[coordination] 前置任务 " + predecessorID + " 未形成可用修复，重新检查代码占用"
		} else {
			a.st.readyBugDependency(id)
		}
		prev := fieldText(rec.Fields[FLog])
		fields := map[string]any{
			FStatus: SBug,
			FLog:    strings.TrimSpace(prev+message+"\n") + "\n",
		}
		_ = a.fs.updateRecord(id, fields)
	}
}

func (a *App) reconcileBugWaiters(records []Record) {
	statusByID := make(map[string]string, len(records))
	for i := range records {
		statusByID[records[i].RecordID] = fieldText(records[i].Fields[FStatus])
	}
	for i := range records {
		if fieldText(records[i].Fields[FStatus]) != SCodeWait {
			continue
		}
		row, ok := a.st.bugCoordination(records[i].RecordID)
		if !ok || row.PredecessorID == "" {
			continue
		}
		switch statusByID[row.PredecessorID] {
		case SMerge, SDone:
			a.wakeBugDependents(row.PredecessorID, true)
			records[i].Fields[FStatus] = SBug
		case SBlocked:
			a.wakeBugDependents(row.PredecessorID, false)
			records[i].Fields[FStatus] = SBug
		}
	}
}

func (a *App) releaseBugCoordination(recordID, state string) {
	a.st.markBugCoordination(recordID, state)
	a.wakeBugDependents(recordID, false)
}

func (c *bugConsole) webCoordination(rec *Record, records []Record) *webBugCoordination {
	if c.app == nil || !isBugRecord(rec) {
		return nil
	}
	row, ok := c.app.st.bugCoordination(rec.RecordID)
	if !ok {
		similarID, score := mostSimilarActiveBug(rec, records, fieldText(rec.Fields[FWorkspace]))
		if similarID == "" {
			return nil
		}
		return &webBugCoordination{
			SimilarTaskID: similarID,
			Similarity:    score,
			Message:       "发现描述相似的处理中任务，AI 调查后会进一步比较代码范围",
		}
	}
	info := &webBugCoordination{
		State:         row.State,
		AffectedFiles: append([]string(nil), row.Files...),
		PredecessorID: row.PredecessorID,
		SimilarTaskID: row.SimilarRecord,
		Similarity:    row.Similarity,
	}
	if row.PredecessorID != "" {
		info.MergeOrder = []string{row.PredecessorID, row.RecordID}
		if row.State == "waiting" {
			info.Message = "与前置任务修改同一代码区域，正在等待其 Review 通过"
		} else {
			info.Message = "当前分支已接在前置任务之后，请按显示顺序合并"
		}
	} else if row.SimilarRecord != "" {
		info.Message = "已发现相似任务；代码范围没有重叠时仍会独立并发"
	}
	return info
}
