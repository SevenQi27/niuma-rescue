package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS record_runs (
    record_id TEXT PRIMARY KEY,
    stage TEXT NOT NULL, status TEXT NOT NULL, state TEXT NOT NULL,
    run_id TEXT NOT NULL, owner_pid INTEGER NOT NULL, owner_host TEXT NOT NULL,
    title TEXT, attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT,
    next_retry_at REAL, claimed_at REAL NOT NULL, heartbeat_at REAL NOT NULL, updated_at REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS run_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT, record_id TEXT, stage TEXT,
    event TEXT, status_from TEXT, status_to TEXT, message TEXT, created_at REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS inbox_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT, event_key TEXT UNIQUE, status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0, handled INTEGER, message_json TEXT NOT NULL,
    last_error TEXT, created_at REAL NOT NULL, updated_at REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS local_records (
    record_id TEXT PRIMARY KEY,
    fields_json TEXT NOT NULL,
    external_id TEXT UNIQUE,
    sync_state TEXT NOT NULL DEFAULT 'local',
    sync_error TEXT,
    remote_modified_at INTEGER NOT NULL DEFAULT 0,
    archived INTEGER NOT NULL DEFAULT 0,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_local_records_active ON local_records(archived, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_local_records_sync ON local_records(sync_state, archived);
CREATE TABLE IF NOT EXISTS external_bindings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    record_id TEXT NOT NULL,
    integration_kind TEXT NOT NULL,
    external_type TEXT,
    external_id TEXT NOT NULL,
    external_key TEXT,
    external_url TEXT,
    sync_state TEXT NOT NULL DEFAULT 'bound',
    last_error TEXT,
    remote_version TEXT,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    UNIQUE(integration_kind, external_id),
    UNIQUE(record_id, integration_kind, external_id)
);
CREATE INDEX IF NOT EXISTS idx_external_bindings_record ON external_bindings(record_id, integration_kind);
CREATE TABLE IF NOT EXISTS integration_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    integration_kind TEXT NOT NULL,
    event_key TEXT NOT NULL,
    summary TEXT,
    status TEXT NOT NULL DEFAULT 'received',
    attempts INTEGER NOT NULL DEFAULT 0,
    payload_json TEXT NOT NULL,
    last_error TEXT,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    UNIQUE(integration_kind, event_key)
);
CREATE INDEX IF NOT EXISTS idx_integration_events_status ON integration_events(status, created_at);
`

type Store struct{ db *sql.DB }

type Claim struct {
	OK          bool
	RunID       string
	Reason      string // "busy" | "retry_wait" | ""
	Attempts    int
	NextRetryAt float64
}

type RunRow struct {
	RecordID, Stage, Status, State, RunID, LastError string
	Attempts                                         int
	NextRetryAt, ClaimedAt, HeartbeatAt, UpdatedAt   float64
}

type IntegrationEventRow struct {
	ID              int64   `json:"id"`
	IntegrationKind string  `json:"integration_kind"`
	EventKey        string  `json:"event_key"`
	Summary         string  `json:"summary"`
	Status          string  `json:"status"`
	Attempts        int     `json:"attempts"`
	LastError       string  `json:"last_error"`
	CreatedAt       float64 `json:"created_at"`
}

func nowf() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func openStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "niuma.sqlite3"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // sqlite：串行化写，省去 busy 重试
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=30000;"); err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) event(runID, recordID, stage, event, from, to, msg string) {
	s.db.Exec(`INSERT INTO run_events (run_id,record_id,stage,event,status_from,status_to,message,created_at)
		VALUES (?,?,?,?,?,?,?,?)`, runID, recordID, stage, event, from, to, msg, nowf())
}

func (s *Store) claim(recordID, stage, status, title string) Claim {
	now := nowf()
	staleBefore := now - float64(cfg.StaleAfter)
	host, _ := os.Hostname()
	// 用真正的事务把连接钉住：SELECT + INSERT 原子化。配合 SetMaxOpenConns(1)，
	// 并发 claim 会排队等连接而不是撞 SQLITE_BUSY。
	tx, err := s.db.Begin()
	if err != nil {
		return Claim{Reason: "db_error"}
	}
	defer tx.Rollback() // Commit 之后是 no-op

	var (
		st, stg, sts string
		attempts     int
		hb           float64
		nextRetry    sql.NullFloat64
	)
	qerr := tx.QueryRow(`SELECT state,stage,status,attempts,heartbeat_at,next_retry_at FROM record_runs WHERE record_id=?`,
		recordID).Scan(&st, &stg, &sts, &attempts, &hb, &nextRetry)
	if qerr == nil { // 已有行
		if st == "processing" && hb >= staleBefore {
			return Claim{Reason: "busy", Attempts: attempts}
		}
		if st == "failed" && nextRetry.Valid && nextRetry.Float64 > now && stg == stage && sts == status {
			return Claim{Reason: "retry_wait", Attempts: attempts, NextRetryAt: nextRetry.Float64}
		}
		attempts++
	} else {
		attempts = 1
	}
	runID := fmt.Sprintf("%s-%s-%d-%d", recordID, stage, int64(now), time.Now().UnixNano()%1e8)
	_, e := tx.Exec(`INSERT INTO record_runs
		(record_id,stage,status,state,run_id,owner_pid,owner_host,title,attempts,last_error,next_retry_at,claimed_at,heartbeat_at,updated_at)
		VALUES (?,?,?,'processing',?,?,?,?,?,NULL,NULL,?,?,?)
		ON CONFLICT(record_id) DO UPDATE SET
		  stage=excluded.stage,status=excluded.status,state='processing',run_id=excluded.run_id,
		  owner_pid=excluded.owner_pid,owner_host=excluded.owner_host,title=excluded.title,
		  attempts=excluded.attempts,last_error=NULL,next_retry_at=NULL,
		  claimed_at=excluded.claimed_at,heartbeat_at=excluded.heartbeat_at,updated_at=excluded.updated_at`,
		recordID, stage, status, runID, os.Getpid(), host, title, attempts, now, now, now)
	if e != nil {
		return Claim{Reason: "db_error"}
	}
	if e := tx.Commit(); e != nil {
		return Claim{Reason: "db_error"}
	}
	s.event(runID, recordID, stage, "claimed", status, "", title)
	return Claim{OK: true, RunID: runID, Attempts: attempts}
}

func (s *Store) heartbeat(runID string) {
	s.db.Exec(`UPDATE record_runs SET heartbeat_at=?,updated_at=? WHERE run_id=?`, nowf(), nowf(), runID)
}

func (s *Store) complete(runID, from, to, msg string) {
	now := nowf()
	rec, stage := s.runMeta(runID)
	s.db.Exec(`UPDATE record_runs SET state='done',status=?,last_error=NULL,next_retry_at=NULL,heartbeat_at=?,updated_at=? WHERE run_id=?`,
		to, now, now, runID)
	s.event(runID, rec, stage, "done", from, to, msg)
}

func (s *Store) fail(runID, errMsg string, retryDelay int) {
	now := nowf()
	var next sql.NullFloat64
	if retryDelay > 0 {
		next = sql.NullFloat64{Float64: now + float64(retryDelay), Valid: true}
	}
	rec, stage := s.runMeta(runID)
	if len(errMsg) > 1000 {
		errMsg = errMsg[:1000]
	}
	s.db.Exec(`UPDATE record_runs SET state='failed',last_error=?,next_retry_at=?,heartbeat_at=?,updated_at=? WHERE run_id=?`,
		errMsg, next, now, now, runID)
	s.event(runID, rec, stage, "failed", "", "", errMsg)
}

func (s *Store) runMeta(runID string) (recordID, stage string) {
	s.db.QueryRow(`SELECT record_id,stage FROM record_runs WHERE run_id=?`, runID).Scan(&recordID, &stage)
	return
}

func (s *Store) clear(recordID, reason string) bool {
	var runID, stage, status string
	if s.db.QueryRow(`SELECT run_id,stage,status FROM record_runs WHERE record_id=?`, recordID).
		Scan(&runID, &stage, &status) != nil {
		return false
	}
	s.event(runID, recordID, stage, "cleared", status, "", reason)
	s.db.Exec(`DELETE FROM record_runs WHERE record_id=?`, recordID)
	return true
}

func (s *Store) listRuns(limit int, state string) []RunRow {
	q := `SELECT record_id,stage,status,state,run_id,COALESCE(last_error,''),attempts,COALESCE(next_retry_at,0),claimed_at,heartbeat_at,updated_at FROM record_runs`
	args := []any{}
	if state != "" {
		q += ` WHERE state=? ORDER BY updated_at DESC LIMIT ?`
		args = append(args, state, limit)
	} else {
		q += ` ORDER BY updated_at DESC LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []RunRow
	for rows.Next() {
		var r RunRow
		rows.Scan(&r.RecordID, &r.Stage, &r.Status, &r.State, &r.RunID, &r.LastError,
			&r.Attempts, &r.NextRetryAt, &r.ClaimedAt, &r.HeartbeatAt, &r.UpdatedAt)
		out = append(out, r)
	}
	return out
}

func (s *Store) enqueueIntegrationEvent(kind, eventKey, summary string, payload []byte) (int64, bool, error) {
	now := nowf()
	res, err := s.db.Exec(`INSERT OR IGNORE INTO integration_events
		(integration_kind,event_key,summary,status,attempts,payload_json,created_at,updated_at)
		VALUES (?,?,?,'received',0,?,?,?)`, kind, eventKey, summary, string(payload), now, now)
	if err != nil {
		return 0, false, err
	}
	inserted, _ := res.RowsAffected()
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM integration_events WHERE integration_kind=? AND event_key=?`, kind, eventKey).Scan(&id); err != nil {
		return 0, false, err
	}
	return id, inserted > 0, nil
}

func (s *Store) listIntegrationEvents(limit int) []IntegrationEventRow {
	if limit < 1 || limit > 200 {
		limit = 30
	}
	rows, err := s.db.Query(`SELECT id,integration_kind,event_key,COALESCE(summary,''),status,attempts,
		COALESCE(last_error,''),created_at FROM integration_events ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var events []IntegrationEventRow
	for rows.Next() {
		var event IntegrationEventRow
		if rows.Scan(&event.ID, &event.IntegrationKind, &event.EventKey, &event.Summary, &event.Status,
			&event.Attempts, &event.LastError, &event.CreatedAt) == nil {
			events = append(events, event)
		}
	}
	return events
}

// ── inbox ────────────────────────────────────────────────────────────
func (s *Store) enqueue(eventKey string, message map[string]any) (int64, bool) {
	now := nowf()
	payload, _ := json.Marshal(message)
	if eventKey == "" {
		if mid, ok := message["message_id"].(string); ok {
			eventKey = mid
		}
	}
	if eventKey != "" {
		res, err := s.db.Exec(`INSERT OR IGNORE INTO inbox_messages (event_key,status,attempts,message_json,created_at,updated_at)
			VALUES (?,?,0,?,?,?)`, eventKey, "pending", string(payload), now, now)
		if err != nil {
			return 0, false
		}
		n, _ := res.RowsAffected()
		var id int64
		s.db.QueryRow(`SELECT id FROM inbox_messages WHERE event_key=?`, eventKey).Scan(&id)
		return id, n == 1
	}
	res, _ := s.db.Exec(`INSERT INTO inbox_messages (event_key,status,attempts,message_json,created_at,updated_at)
		VALUES (NULL,'pending',0,?,?,?)`, string(payload), now, now)
	id, _ := res.LastInsertId()
	return id, true
}

func (s *Store) processPending(handler func(map[string]any) bool, trigger func(), limit int) int {
	now := nowf()
	// 把 stale 的 processing 退回 pending
	s.db.Exec(`UPDATE inbox_messages SET status='pending',updated_at=? WHERE status='processing' AND updated_at < ?`,
		now, now-600)
	rows, err := s.db.Query(`SELECT id,message_json FROM inbox_messages WHERE status='pending' ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return 0
	}
	type item struct {
		id  int64
		msg map[string]any
	}
	var items []item
	for rows.Next() {
		var id int64
		var mj string
		rows.Scan(&id, &mj)
		var m map[string]any
		json.Unmarshal([]byte(mj), &m)
		items = append(items, item{id, m})
	}
	rows.Close()
	count := 0
	for _, it := range items {
		s.db.Exec(`UPDATE inbox_messages SET status='processing',attempts=attempts+1,updated_at=? WHERE id=?`, nowf(), it.id)
		func() {
			defer func() {
				if r := recover(); r != nil {
					s.db.Exec(`UPDATE inbox_messages SET status='failed',last_error=?,updated_at=? WHERE id=?`,
						fmt.Sprintf("%v", r), nowf(), it.id)
				}
			}()
			handled := handler(it.msg)
			h := 0
			if handled {
				h = 1
			}
			s.db.Exec(`UPDATE inbox_messages SET status='done',handled=?,last_error=NULL,updated_at=? WHERE id=?`, h, nowf(), it.id)
			if handled {
				trigger()
			}
		}()
		count++
	}
	return count
}
