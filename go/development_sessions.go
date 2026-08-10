package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type preparedCodeTask struct {
	Workspace  Workspace
	Worktree   string
	Branch     string
	CompareRef string
	SessionID  string
	TaskState  string
	CommitSHA  string
}

type developmentSession struct {
	SessionID   string                   `json:"session_id"`
	Workspace   string                   `json:"workspace"`
	RolloverKey string                   `json:"rollover_key"`
	Branch      string                   `json:"branch"`
	Worktree    string                   `json:"worktree"`
	BaseRef     string                   `json:"base_ref"`
	BaseSHA     string                   `json:"base_sha"`
	State       string                   `json:"state"`
	CreatedAt   float64                  `json:"created_at"`
	ClosedAt    float64                  `json:"closed_at,omitempty"`
	UpdatedAt   float64                  `json:"updated_at"`
	Tasks       []developmentSessionTask `json:"tasks,omitempty"`
}

type developmentSessionTask struct {
	RecordID  string  `json:"record_id"`
	SessionID string  `json:"session_id"`
	TaskKind  string  `json:"task_kind"`
	Sequence  int     `json:"sequence"`
	Branch    string  `json:"branch"`
	Worktree  string  `json:"worktree"`
	StartSHA  string  `json:"start_sha"`
	CommitSHA string  `json:"commit_sha,omitempty"`
	State     string  `json:"state"`
	CreatedAt float64 `json:"created_at"`
	UpdatedAt float64 `json:"updated_at"`
}

func developmentSessionRolloverKey(ws Workspace, now time.Time) string {
	if strings.EqualFold(strings.TrimSpace(ws.SessionRollover), "daily") {
		return now.Format("20060102")
	}
	return "manual"
}

func developmentSessionID(ws Workspace, rolloverKey string, now time.Time) string {
	key := strings.Trim(strings.ReplaceAll(ws.safeKey(), "_", "-"), "-")
	if key == "" {
		key = "workspace"
	}
	if rolloverKey == "manual" {
		rolloverKey = now.Format("20060102-150405")
	}
	return fmt.Sprintf("%s-%s", key, rolloverKey)
}

func (s *Store) developmentSession(sessionID string) (developmentSession, bool) {
	var row developmentSession
	var closed sql.NullFloat64
	err := s.db.QueryRow(`SELECT session_id,workspace,rollover_key,branch,worktree,base_ref,base_sha,state,
		created_at,closed_at,updated_at FROM development_sessions WHERE session_id=?`, sessionID).Scan(
		&row.SessionID, &row.Workspace, &row.RolloverKey, &row.Branch, &row.Worktree,
		&row.BaseRef, &row.BaseSHA, &row.State, &row.CreatedAt, &closed, &row.UpdatedAt)
	if err != nil {
		return developmentSession{}, false
	}
	if closed.Valid {
		row.ClosedAt = closed.Float64
	}
	row.Tasks = s.developmentSessionTasks(row.SessionID)
	return row, true
}

func (s *Store) openDevelopmentSession(workspace, rolloverKey string) (developmentSession, bool) {
	var sessionID string
	err := s.db.QueryRow(`SELECT session_id FROM development_sessions
		WHERE workspace=? AND rollover_key=? AND state='open' ORDER BY created_at DESC LIMIT 1`,
		workspace, rolloverKey).Scan(&sessionID)
	if err != nil {
		return developmentSession{}, false
	}
	return s.developmentSession(sessionID)
}

func (s *Store) saveDevelopmentSession(row developmentSession) error {
	now := nowf()
	if row.CreatedAt == 0 {
		row.CreatedAt = now
	}
	_, err := s.db.Exec(`INSERT INTO development_sessions
		(session_id,workspace,rollover_key,branch,worktree,base_ref,base_sha,state,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?, 'open', ?, ?)`, row.SessionID, row.Workspace, row.RolloverKey,
		row.Branch, row.Worktree, row.BaseRef, row.BaseSHA, row.CreatedAt, now)
	return err
}

func (s *Store) bindDevelopmentTask(recordID, sessionID, kind, branch, worktree, startSHA string) (developmentSessionTask, error) {
	if task, ok := s.developmentTask(recordID); ok {
		return task, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return developmentSessionTask{}, err
	}
	defer tx.Rollback()
	var sequence int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(sequence),0)+1 FROM development_session_tasks WHERE session_id=?`, sessionID).Scan(&sequence); err != nil {
		return developmentSessionTask{}, err
	}
	now := nowf()
	if _, err := tx.Exec(`INSERT INTO development_session_tasks
		(record_id,session_id,task_kind,sequence,branch,worktree,start_sha,state,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,'editing',?,?)`, recordID, sessionID, kind, sequence, branch, worktree, startSHA, now, now); err != nil {
		return developmentSessionTask{}, err
	}
	if _, err := tx.Exec(`UPDATE development_sessions SET updated_at=? WHERE session_id=?`, now, sessionID); err != nil {
		return developmentSessionTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return developmentSessionTask{}, err
	}
	return developmentSessionTask{RecordID: recordID, SessionID: sessionID, TaskKind: kind,
		Sequence: sequence, Branch: branch, Worktree: worktree, StartSHA: startSHA,
		State: "editing", CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) developmentTask(recordID string) (developmentSessionTask, bool) {
	var row developmentSessionTask
	err := s.db.QueryRow(`SELECT record_id,session_id,task_kind,sequence,branch,worktree,start_sha,COALESCE(commit_sha,''),state,
		created_at,updated_at FROM development_session_tasks WHERE record_id=?`, recordID).Scan(
		&row.RecordID, &row.SessionID, &row.TaskKind, &row.Sequence, &row.Branch, &row.Worktree,
		&row.StartSHA, &row.CommitSHA,
		&row.State, &row.CreatedAt, &row.UpdatedAt)
	return row, err == nil
}

func (s *Store) developmentSessionTasks(sessionID string) []developmentSessionTask {
	rows, err := s.db.Query(`SELECT record_id,session_id,task_kind,sequence,branch,worktree,start_sha,COALESCE(commit_sha,''),state,
		created_at,updated_at FROM development_session_tasks WHERE session_id=? ORDER BY sequence`, sessionID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []developmentSessionTask
	for rows.Next() {
		var row developmentSessionTask
		if rows.Scan(&row.RecordID, &row.SessionID, &row.TaskKind, &row.Sequence, &row.Branch,
			&row.Worktree, &row.StartSHA,
			&row.CommitSHA, &row.State, &row.CreatedAt, &row.UpdatedAt) == nil {
			out = append(out, row)
		}
	}
	return out
}

func (s *Store) markDevelopmentTask(recordID, commitSHA, state string) {
	now := nowf()
	_, _ = s.db.Exec(`UPDATE development_session_tasks SET commit_sha=CASE WHEN ?='' THEN commit_sha ELSE ? END,
		state=?,updated_at=? WHERE record_id=?`, commitSHA, commitSHA, state, now, recordID)
	_, _ = s.db.Exec(`UPDATE development_sessions SET updated_at=? WHERE session_id=(
		SELECT session_id FROM development_session_tasks WHERE record_id=?)`, now, recordID)
}

func (s *Store) rebaseDevelopmentTask(recordID, startSHA, commitSHA string) {
	_, _ = s.db.Exec(`UPDATE development_session_tasks SET start_sha=?,commit_sha=?,updated_at=? WHERE record_id=?`,
		startSHA, commitSHA, nowf(), recordID)
}

func (s *Store) closeDevelopmentSession(sessionID string) error {
	res, err := s.db.Exec(`UPDATE development_sessions SET state='frozen',closed_at=?,updated_at=?
		WHERE session_id=? AND state='open'`, nowf(), nowf(), sessionID)
	if err != nil {
		return err
	}
	changed, _ := res.RowsAffected()
	if changed == 0 {
		return errf("开发会话不存在或已经关闭")
	}
	return nil
}

func (s *Store) pendingDevelopmentTasks(sessionID string) []developmentSessionTask {
	var pending []developmentSessionTask
	for _, task := range s.developmentSessionTasks(sessionID) {
		if task.State != "integrated" {
			pending = append(pending, task)
		}
	}
	return pending
}

func (s *Store) listDevelopmentSessions(limit int) []developmentSession {
	if limit < 1 || limit > 100 {
		limit = 30
	}
	rows, err := s.db.Query(`SELECT session_id FROM development_sessions ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	out := make([]developmentSession, 0, len(ids))
	for _, id := range ids {
		if row, ok := s.developmentSession(id); ok {
			out = append(out, row)
		}
	}
	return out
}

func (a *App) prepareCodeTask(ws Workspace, kind, recordID string) (preparedCodeTask, error) {
	if !ws.sharedSession() {
		if kind == "BUG" {
			isolated, worktree, branch, err := scmPrepareBug(ws, recordID)
			return preparedCodeTask{Workspace: isolated, Worktree: worktree, Branch: branch, CompareRef: isolated.BaseRef}, err
		}
		worktree, branch, err := scmPrepare(ws, recordID)
		return preparedCodeTask{Workspace: ws, Worktree: worktree, Branch: branch, CompareRef: ws.BaseRef}, err
	}
	if !strings.EqualFold(strings.TrimSpace(ws.SCM), "git") {
		return preparedCodeTask{}, errf("共享开发会话当前只支持 git workspace")
	}
	if task, ok := a.st.developmentTask(recordID); ok {
		session, found := a.st.developmentSession(task.SessionID)
		if !found {
			return preparedCodeTask{}, errf("任务关联的开发会话 %s 不存在", task.SessionID)
		}
		if session.State != "open" {
			return preparedCodeTask{}, errf("任务关联的开发会话 %s 已关闭，不能继续写入", session.SessionID)
		}
		worktree, branch, err := scmPrepareSessionTask(ws, session.SessionID, task.TaskKind, recordID, task.StartSHA)
		if err != nil {
			return preparedCodeTask{}, err
		}
		if filepath.Clean(worktree) != filepath.Clean(task.Worktree) || branch != task.Branch {
			return preparedCodeTask{}, errf("任务 worktree 与会话记录不一致")
		}
		return preparedCodeTask{Workspace: ws, Worktree: worktree, Branch: branch,
			CompareRef: task.StartSHA, SessionID: session.SessionID, TaskState: task.State, CommitSHA: task.CommitSHA}, nil
	}

	now := time.Now()
	rolloverKey := developmentSessionRolloverKey(ws, now)
	session, ok := a.st.openDevelopmentSession(ws.Key, rolloverKey)
	if !ok {
		sessionID := developmentSessionID(ws, rolloverKey, now)
		if _, exists := a.st.developmentSession(sessionID); exists {
			sessionID += fmt.Sprintf("-%09d", now.UnixNano()%1_000_000_000)
		}
		worktree, branch, err := scmPrepareSession(ws, sessionID)
		if err != nil {
			return preparedCodeTask{}, err
		}
		baseSHA, err := git(worktree, "rev-parse", "HEAD")
		if err != nil {
			return preparedCodeTask{}, errf("读取会话基线失败: %s", strings.TrimSpace(baseSHA))
		}
		session = developmentSession{
			SessionID: sessionID, Workspace: ws.Key, RolloverKey: rolloverKey,
			Branch: branch, Worktree: worktree, BaseRef: ws.BaseRef, BaseSHA: strings.TrimSpace(baseSHA), State: "open",
		}
		if err := a.st.saveDevelopmentSession(session); err != nil {
			return preparedCodeTask{}, errf("保存开发会话失败: %v", err)
		}
	}
	if files := productWorkingTreeChanges(session.Worktree); len(files) > 0 {
		return preparedCodeTask{}, errf("开发会话 %s 的集成 worktree 不干净：%s", session.SessionID, strings.Join(files, ", "))
	}
	startSHA, err := git(session.Worktree, "rev-parse", "HEAD")
	if err != nil {
		return preparedCodeTask{}, errf("读取开发会话 HEAD 失败: %s", strings.TrimSpace(startSHA))
	}
	startSHA = strings.TrimSpace(startSHA)
	worktree, branch, err := scmPrepareSessionTask(ws, session.SessionID, kind, recordID, startSHA)
	if err != nil {
		return preparedCodeTask{}, err
	}
	task, err := a.st.bindDevelopmentTask(recordID, session.SessionID, kind, branch, worktree, startSHA)
	if err != nil {
		return preparedCodeTask{}, errf("任务加入开发会话失败: %v", err)
	}
	return preparedCodeTask{Workspace: ws, Worktree: worktree, Branch: branch,
		CompareRef: startSHA, SessionID: session.SessionID, TaskState: task.State}, nil
}

func (a *App) commitDevelopmentTask(prepared preparedCodeTask, recordID, message string) (string, error) {
	if err := gitCommitAllChecked(prepared.Worktree, message); err != nil {
		return "", err
	}
	head, err := git(prepared.Worktree, "rev-parse", "HEAD")
	if err != nil {
		return "", errf("读取任务提交失败: %s", strings.TrimSpace(head))
	}
	head = strings.TrimSpace(head)
	if prepared.SessionID != "" {
		a.st.markDevelopmentTask(recordID, head, "developed")
	}
	return head, nil
}

func (a *App) integrateDevelopmentTask(prepared preparedCodeTask, recordID string) (developmentSession, error) {
	if prepared.SessionID == "" {
		return developmentSession{Branch: prepared.Branch, Worktree: prepared.Worktree, State: "task"}, nil
	}
	task, ok := a.st.developmentTask(recordID)
	if !ok || strings.TrimSpace(task.CommitSHA) == "" {
		return developmentSession{}, errf("任务还没有可集成的提交")
	}
	session, ok := a.st.developmentSession(task.SessionID)
	if !ok || session.State != "open" {
		return developmentSession{}, errf("开发会话不存在或已经关闭")
	}
	if files := productWorkingTreeChanges(session.Worktree); len(files) > 0 {
		return developmentSession{}, errf("会话集成 worktree 不干净：%s", strings.Join(files, ", "))
	}
	if files := productWorkingTreeChanges(task.Worktree); len(files) > 0 {
		return developmentSession{}, errf("任务 worktree 尚有未提交改动：%s", strings.Join(files, ", "))
	}
	sessionHead, err := git(session.Worktree, "rev-parse", "HEAD")
	if err != nil {
		return developmentSession{}, errf("读取会话 HEAD 失败: %s", strings.TrimSpace(sessionHead))
	}
	sessionHead = strings.TrimSpace(sessionHead)
	if sessionHead != task.StartSHA {
		out, rebaseErr := git(task.Worktree, "rebase", "--onto", session.Branch, task.StartSHA, task.Branch)
		if rebaseErr != nil {
			_, _ = git(task.Worktree, "rebase", "--abort")
			a.st.markDevelopmentTask(recordID, task.CommitSHA, "integration_conflict")
			return developmentSession{}, errf("任务与会话最新代码冲突，仅暂停当前任务: %s", tail(strings.TrimSpace(out), 1200))
		}
		rebasedHead, headErr := git(task.Worktree, "rev-parse", "HEAD")
		if headErr != nil {
			return developmentSession{}, errf("读取 rebase 后提交失败: %s", strings.TrimSpace(rebasedHead))
		}
		task.CommitSHA = strings.TrimSpace(rebasedHead)
		task.StartSHA = sessionHead
		a.st.rebaseDevelopmentTask(recordID, task.StartSHA, task.CommitSHA)
	}
	if out, err := git(session.Worktree, "merge", "--ff-only", task.Branch); err != nil {
		return developmentSession{}, errf("任务无法快进到共享会话: %s", strings.TrimSpace(out))
	}
	if err := ensureBranchNoUpstream(prepared.Workspace.Path, session.Branch); err != nil {
		return developmentSession{}, err
	}
	a.st.markDevelopmentTask(recordID, task.CommitSHA, "integrated")
	return session, nil
}

func (a *App) freezeDevelopmentSession(sessionID string) error {
	a.gitMu.Lock()
	defer a.gitMu.Unlock()
	session, ok := a.st.developmentSession(sessionID)
	if !ok || session.State != "open" {
		return errf("开发会话不存在或已经关闭")
	}
	if pending := a.st.pendingDevelopmentTasks(sessionID); len(pending) > 0 {
		ids := make([]string, 0, len(pending))
		for _, task := range pending {
			ids = append(ids, task.RecordID+"("+task.State+")")
		}
		return errf("仍有任务尚未进入会话分支：%s", strings.Join(ids, ", "))
	}
	if files := productWorkingTreeChanges(session.Worktree); len(files) > 0 {
		return errf("会话集成 worktree 不干净：%s", strings.Join(files, ", "))
	}
	return a.st.closeDevelopmentSession(sessionID)
}
