package eventlog

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	// Pure Go, so no cgo and the ONCE server gets a plain cross-compiled binary. Importing it
	// registers the "sqlite" driver.
	sqlite3 "modernc.org/sqlite"
)

// sqliteConstraintUnique is SQLITE_CONSTRAINT_UNIQUE, raised when once_key collides.
const sqliteConstraintUnique = 2067

// Ordinary events leave once_key NULL. SQLite treats NULLs as distinct in a unique index, so they
// never collide, while once-only events do. The idempotency invariant that the whole re-import
// story rests on is therefore enforced by storage rather than by whoever remembered to check.
const schema = `
CREATE TABLE IF NOT EXISTS events (
  seq        INTEGER PRIMARY KEY AUTOINCREMENT,
  id         TEXT NOT NULL UNIQUE,
  time       TEXT NOT NULL,
  collection TEXT NOT NULL,
  record_id  TEXT NOT NULL,
  action     TEXT NOT NULL,
  version    INTEGER NOT NULL,
  actor      TEXT NOT NULL,
  data       TEXT NOT NULL,
  once_key   TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS events_once ON events (once_key);
`

// SQLite holds the log on disk. This is money, so durability is not left at its defaults.
type SQLite struct{ db *sql.DB }

func OpenSQLite(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open %s: %w", path, err)
	}

	// One writer, so a second connection could only ever contend with the first.
	db.SetMaxOpenConns(1)

	// synchronous=FULL costs an fsync per commit and buys back the last transaction on power
	// loss. WAL's default of NORMAL trades that away, which is the wrong trade for a ledger.
	for _, pragma := range []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = FULL`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("eventlog: %s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("eventlog: schema: %w", err)
	}
	return &SQLite{db: db}, nil
}

func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) Append(e Event) error { return s.insert(e, nil) }

func (s *SQLite) AppendOnce(e Event) error {
	key := onceKey(e)
	err := s.insert(e, &key)

	var serr *sqlite3.Error
	if errors.As(err, &serr) && serr.Code() == sqliteConstraintUnique {
		return ErrAlreadyTracked
	}
	return err
}

func (s *SQLite) insert(e Event, once *string) error {
	_, err := s.db.Exec(
		`INSERT INTO events (id, time, collection, record_id, action, version, actor, data, once_key)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Time.UTC().Format(time.RFC3339Nano), e.Collection, e.RecordID,
		e.Action, e.Version, e.Actor, string(e.Data), once,
	)
	return err
}

func (s *SQLite) All() ([]Event, error) {
	rows, err := s.db.Query(
		`SELECT id, time, collection, record_id, action, version, actor, data
		 FROM events ORDER BY seq`)
	if err != nil {
		return nil, fmt.Errorf("eventlog: read: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var stamp, data string
		if err := rows.Scan(&e.ID, &stamp, &e.Collection, &e.RecordID, &e.Action, &e.Version, &e.Actor, &data); err != nil {
			return nil, fmt.Errorf("eventlog: scan: %w", err)
		}
		if e.Time, err = time.Parse(time.RFC3339Nano, stamp); err != nil {
			return nil, fmt.Errorf("eventlog: event %s has an unreadable time %q: %w", e.ID, stamp, err)
		}
		e.Data = json.RawMessage(data)
		events = append(events, e)
	}
	return events, rows.Err()
}
