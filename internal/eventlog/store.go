package eventlog

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp   TEXT NOT NULL,
    type        TEXT NOT NULL,
    app_id      TEXT,
    cluster_id  TEXT,
    summary     TEXT NOT NULL,
    detail_json TEXT
);

CREATE TABLE IF NOT EXISTS state_snapshots (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp      TEXT NOT NULL,
    cluster_id     TEXT NOT NULL,
    total_gpus     INTEGER NOT NULL,
    allocated_gpus INTEGER NOT NULL,
    free_gpus      INTEGER NOT NULL,
    replica_count  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events(timestamp);
CREATE INDEX IF NOT EXISTS idx_events_type ON events(type);
CREATE INDEX IF NOT EXISTS idx_snapshots_timestamp ON state_snapshots(timestamp);
`

const retentionHours = 24

// Store is a SQLite-backed event log.
type Store struct {
	db *sql.DB
}

// NewStore opens or creates a SQLite database at dbPath and initializes the schema.
// Use ":memory:" for an in-memory database (useful for testing).
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	s := &Store{db: db}
	s.pruneOldEvents()
	return s, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// LogEvent inserts an event record.
func (s *Store) LogEvent(ctx context.Context, e EventRecord) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO events (timestamp, type, app_id, cluster_id, summary, detail_json)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		e.Timestamp.Format(time.RFC3339Nano),
		e.Type,
		e.AppID,
		e.ClusterID,
		e.Summary,
		e.DetailJSON,
	)
	return err
}

// LogStateSnapshot inserts a batch of cluster snapshots.
func (s *Store) LogStateSnapshot(ctx context.Context, snaps []ClusterSnapshot) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO state_snapshots (timestamp, cluster_id, total_gpus, allocated_gpus, free_gpus, replica_count)
		 VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, snap := range snaps {
		_, err := stmt.ExecContext(ctx,
			snap.Timestamp.Format(time.RFC3339Nano),
			snap.ClusterID,
			snap.TotalGPUs,
			snap.AllocatedGPUs,
			snap.FreeGPUs,
			snap.ReplicaCount,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// QueryEvents returns events since the given time, up to limit.
func (s *Store) QueryEvents(ctx context.Context, since time.Time, limit int) ([]EventRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, timestamp, type, app_id, cluster_id, summary, detail_json
		 FROM events
		 WHERE timestamp >= ?
		 ORDER BY timestamp DESC
		 LIMIT ?`,
		since.Format(time.RFC3339Nano),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []EventRecord
	for rows.Next() {
		var e EventRecord
		var ts string
		var appID, clusterID, detailJSON sql.NullString
		if err := rows.Scan(&e.ID, &ts, &e.Type, &appID, &clusterID, &e.Summary, &detailJSON); err != nil {
			return nil, err
		}
		e.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		e.AppID = appID.String
		e.ClusterID = clusterID.String
		e.DetailJSON = detailJSON.String
		events = append(events, e)
	}
	return events, rows.Err()
}

// QuerySnapshots returns GPU utilization snapshots for a cluster since the given time.
func (s *Store) QuerySnapshots(ctx context.Context, clusterID string, since time.Time) ([]ClusterSnapshot, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, timestamp, cluster_id, total_gpus, allocated_gpus, free_gpus, replica_count
		 FROM state_snapshots
		 WHERE cluster_id = ? AND timestamp >= ?
		 ORDER BY timestamp ASC`,
		clusterID,
		since.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var snaps []ClusterSnapshot
	for rows.Next() {
		var snap ClusterSnapshot
		var ts string
		if err := rows.Scan(&snap.ID, &ts, &snap.ClusterID, &snap.TotalGPUs, &snap.AllocatedGPUs, &snap.FreeGPUs, &snap.ReplicaCount); err != nil {
			return nil, err
		}
		snap.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		snaps = append(snaps, snap)
	}
	return snaps, rows.Err()
}

func (s *Store) pruneOldEvents() {
	cutoff := time.Now().Add(-retentionHours * time.Hour).Format(time.RFC3339Nano)
	s.db.Exec("DELETE FROM events WHERE timestamp < ?", cutoff)
	s.db.Exec("DELETE FROM state_snapshots WHERE timestamp < ?", cutoff)
}
