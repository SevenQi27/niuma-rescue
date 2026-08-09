package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

type recordSyncMeta struct {
	RecordID         string  `json:"record_id"`
	ExternalID       string  `json:"external_id"`
	SyncState        string  `json:"sync_state"`
	SyncError        string  `json:"sync_error"`
	RemoteModifiedAt int64   `json:"remote_modified_at"`
	Archived         bool    `json:"archived"`
	CreatedAt        float64 `json:"created_at"`
	UpdatedAt        float64 `json:"updated_at"`
}

type localRecords struct{ db *sql.DB }

func newLocalRecords(st *Store) *localRecords { return &localRecords{db: st.db} }

func cloneFields(fields map[string]any) map[string]any {
	out := make(map[string]any, len(fields))
	for key, value := range fields {
		out[key] = value
	}
	return out
}

func newLocalRecordID() string {
	b := make([]byte, 9)
	if _, err := rand.Read(b); err == nil {
		return "local_" + hex.EncodeToString(b)
	}
	return "local_" + itoa(int(time.Now().UnixNano()))
}

func (l *localRecords) listRecords() ([]Record, error) {
	rows, err := l.db.Query(`SELECT record_id,fields_json FROM local_records WHERE archived=0 ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		fields := map[string]any{}
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			return nil, err
		}
		records = append(records, Record{RecordID: id, Fields: fields})
	}
	return records, rows.Err()
}

func (l *localRecords) getRecord(id string) (*Record, error) {
	var raw string
	if err := l.db.QueryRow(`SELECT fields_json FROM local_records WHERE record_id=? AND archived=0`, id).Scan(&raw); err != nil {
		return nil, err
	}
	fields := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, err
	}
	return &Record{RecordID: id, Fields: fields}, nil
}

func (l *localRecords) createRecord(fields map[string]any, syncState string) (*Record, error) {
	id := newLocalRecordID()
	now := nowf()
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	if syncState == "" {
		syncState = "local"
	}
	_, err = l.db.Exec(`INSERT INTO local_records(record_id,fields_json,sync_state,created_at,updated_at) VALUES(?,?,?,?,?)`,
		id, string(raw), syncState, now, now)
	if err != nil {
		return nil, err
	}
	return &Record{RecordID: id, Fields: cloneFields(fields)}, nil
}

func (l *localRecords) updateRecord(id string, fields map[string]any, markPending bool) error {
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	if err := tx.QueryRow(`SELECT fields_json FROM local_records WHERE record_id=? AND archived=0`, id).Scan(&raw); err != nil {
		return err
	}
	current := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &current); err != nil {
		return err
	}
	for key, value := range fields {
		current[key] = value
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		return err
	}
	if markPending {
		_, err = tx.Exec(`UPDATE local_records SET fields_json=?,sync_state='pending',sync_error=NULL,updated_at=? WHERE record_id=?`, string(encoded), nowf(), id)
	} else {
		_, err = tx.Exec(`UPDATE local_records SET fields_json=?,updated_at=? WHERE record_id=?`, string(encoded), nowf(), id)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (l *localRecords) importRemote(record Record) error {
	if record.RecordID == "" {
		return errf("飞书记录缺少 record_id")
	}
	raw, err := json.Marshal(record.Fields)
	if err != nil {
		return err
	}
	now := nowf()
	_, err = l.db.Exec(`INSERT INTO local_records
		(record_id,fields_json,external_id,sync_state,sync_error,remote_modified_at,archived,created_at,updated_at)
		VALUES(?,?,?,'synced',NULL,?,0,?,?)
		ON CONFLICT(record_id) DO UPDATE SET fields_json=excluded.fields_json,external_id=excluded.external_id,
		 sync_state='synced',sync_error=NULL,remote_modified_at=excluded.remote_modified_at,updated_at=excluded.updated_at`,
		record.RecordID, string(raw), record.RecordID, int64(record.LastModifiedTime), now, now)
	return err
}

func (l *localRecords) importRemoteForLocal(localID string, record Record) error {
	raw, err := json.Marshal(record.Fields)
	if err != nil {
		return err
	}
	_, err = l.db.Exec(`UPDATE local_records SET fields_json=?,external_id=?,sync_state='synced',sync_error=NULL,
		remote_modified_at=?,updated_at=? WHERE record_id=?`, string(raw), record.RecordID, int64(record.LastModifiedTime), nowf(), localID)
	return err
}

func (l *localRecords) pendingRecords() ([]Record, error) {
	rows, err := l.db.Query(`SELECT record_id,fields_json FROM local_records
		WHERE archived=0 AND (external_id IS NULL OR sync_state IN ('local','pending','error')) ORDER BY updated_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		fields := map[string]any{}
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			return nil, err
		}
		records = append(records, Record{RecordID: id, Fields: fields})
	}
	return records, rows.Err()
}

func (l *localRecords) meta(id string) (recordSyncMeta, error) {
	var m recordSyncMeta
	var archived int
	err := l.db.QueryRow(`SELECT record_id,COALESCE(external_id,''),sync_state,COALESCE(sync_error,''),remote_modified_at,archived,created_at,updated_at
		FROM local_records WHERE record_id=?`, id).Scan(&m.RecordID, &m.ExternalID, &m.SyncState, &m.SyncError,
		&m.RemoteModifiedAt, &archived, &m.CreatedAt, &m.UpdatedAt)
	m.Archived = archived != 0
	return m, err
}

func (l *localRecords) metaByExternal(externalID string) (recordSyncMeta, error) {
	var id string
	if err := l.db.QueryRow(`SELECT record_id FROM local_records WHERE external_id=?`, externalID).Scan(&id); err != nil {
		return recordSyncMeta{}, err
	}
	return l.meta(id)
}

func (l *localRecords) markSynced(id, externalID string, remoteModified int64) error {
	if remoteModified <= 0 {
		remoteModified = time.Now().UnixMilli()
	}
	_, err := l.db.Exec(`UPDATE local_records SET external_id=?,sync_state='synced',sync_error=NULL,remote_modified_at=?,updated_at=? WHERE record_id=?`,
		externalID, remoteModified, nowf(), id)
	return err
}

func (l *localRecords) markSyncError(id string, syncErr error) {
	message := "同步失败"
	if syncErr != nil {
		message = strings.TrimSpace(syncErr.Error())
	}
	if len(message) > 1000 {
		message = message[:1000]
	}
	l.db.Exec(`UPDATE local_records SET sync_state='error',sync_error=?,updated_at=? WHERE record_id=?`, message, nowf(), id)
}

func (l *localRecords) archive(id string) error {
	res, err := l.db.Exec(`UPDATE local_records SET archived=1,updated_at=? WHERE record_id=?`, nowf(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
