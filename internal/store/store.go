// Package store persists learned messages in SQLite.
//
// Every method that reads or deletes data takes a room ID and filters on it;
// there is deliberately no way to read rows from more than one room at once.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

const schema = `
CREATE TABLE IF NOT EXISTS messages (
	event_id TEXT PRIMARY KEY,
	room_id  TEXT NOT NULL,
	sender   TEXT NOT NULL,
	body     TEXT NOT NULL,
	ts       INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS messages_room_ts ON messages (room_id, ts);
`

type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path. The file is
// created with mode 0600 because it holds plaintext from encrypted rooms.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	db, err := sql.Open("sqlite3", "file:"+path+"?_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Add stores one message. Re-adding the same event ID is a no-op; it reports
// whether a new row was inserted.
func (s *Store) Add(ctx context.Context, roomID, eventID, sender, body string, ts int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO messages (event_id, room_id, sender, body, ts) VALUES (?, ?, ?, ?, ?)`,
		eventID, roomID, sender, body, ts)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Prune keeps only the newest max messages of a room. max <= 0 disables it.
func (s *Store) Prune(ctx context.Context, roomID string, max int) error {
	if max <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM messages WHERE room_id = ?1 AND event_id NOT IN (
			SELECT event_id FROM messages WHERE room_id = ?1 ORDER BY ts DESC LIMIT ?2
		)`, roomID, max)
	return err
}

// ForEach calls fn with each stored body of one room, oldest first.
func (s *Store) ForEach(ctx context.Context, roomID string, fn func(body string)) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM messages WHERE room_id = ? ORDER BY ts`, roomID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var body string
		if err = rows.Scan(&body); err != nil {
			return n, err
		}
		fn(body)
		n++
	}
	return n, rows.Err()
}

// Count returns the number of stored messages in one room.
func (s *Store) Count(ctx context.Context, roomID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE room_id = ?`, roomID).Scan(&n)
	return n, err
}

// DeleteRoom forgets everything learned in one room.
func (s *Store) DeleteRoom(ctx context.Context, roomID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE room_id = ?`, roomID)
	return err
}
