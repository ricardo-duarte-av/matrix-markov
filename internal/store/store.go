// Package store persists learned messages in SQLite.
//
// Every method that reads or deletes data takes a room ID and filters on it;
// there is deliberately no way to read rows from more than one room at once.
package store

import (
	"context"
	"database/sql"
	"errors"
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
CREATE TABLE IF NOT EXISTS reactions (
	event_id        TEXT PRIMARY KEY,
	room_id         TEXT NOT NULL,
	target_event_id TEXT NOT NULL,
	sender          TEXT NOT NULL,
	key             TEXT NOT NULL,
	shortcode       TEXT NOT NULL DEFAULT '',
	ts              INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS reactions_room_target ON reactions (room_id, target_event_id);
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

// Prune keeps only the newest max messages of a room, and drops reactions
// whose message is gone. max <= 0 disables it.
func (s *Store) Prune(ctx context.Context, roomID string, max int) error {
	if max <= 0 {
		return nil
	}
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM messages WHERE room_id = ?1 AND event_id NOT IN (
			SELECT event_id FROM messages WHERE room_id = ?1 ORDER BY ts DESC LIMIT ?2
		)`, roomID, max)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		_, err = s.db.ExecContext(ctx, `
			DELETE FROM reactions WHERE room_id = ?1 AND target_event_id NOT IN (
				SELECT event_id FROM messages WHERE room_id = ?1
			)`, roomID)
	}
	return err
}

// Message returns the stored body of one message in one room.
func (s *Store) Message(ctx context.Context, roomID, eventID string) (string, bool, error) {
	var body string
	err := s.db.QueryRowContext(ctx, `SELECT body FROM messages WHERE room_id = ? AND event_id = ?`, roomID, eventID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return body, err == nil, err
}

// DeleteMessage removes one message and the reactions on it. It reports
// whether the message existed.
func (s *Store) DeleteMessage(ctx context.Context, roomID, eventID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE room_id = ? AND event_id = ?`, roomID, eventID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	_, err = s.db.ExecContext(ctx, `DELETE FROM reactions WHERE room_id = ? AND target_event_id = ?`, roomID, eventID)
	return n > 0, err
}

// Reaction is one stored reaction joined with the body of the message it
// was put on.
type Reaction struct {
	EventID, Target, Sender, Key, Shortcode, TargetBody string
}

// AddReaction stores one reaction. Re-adding the same event ID is a no-op.
func (s *Store) AddReaction(ctx context.Context, roomID string, r Reaction, ts int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO reactions (event_id, room_id, target_event_id, sender, key, shortcode, ts) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.EventID, roomID, r.Target, r.Sender, r.Key, r.Shortcode, ts)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteReaction removes one reaction and returns it, if it existed.
func (s *Store) DeleteReaction(ctx context.Context, roomID, eventID string) (*Reaction, error) {
	r := Reaction{EventID: eventID}
	err := s.db.QueryRowContext(ctx,
		`SELECT target_event_id, sender, key, shortcode FROM reactions WHERE room_id = ? AND event_id = ?`, roomID, eventID).
		Scan(&r.Target, &r.Sender, &r.Key, &r.Shortcode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM reactions WHERE room_id = ? AND event_id = ?`, roomID, eventID)
	return &r, err
}

// ForEachReaction calls fn for each reaction in one room whose target
// message is stored in that same room.
func (s *Store) ForEachReaction(ctx context.Context, roomID string, fn func(Reaction)) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.event_id, r.target_event_id, r.sender, r.key, r.shortcode, m.body
		FROM reactions r JOIN messages m ON m.event_id = r.target_event_id AND m.room_id = r.room_id
		WHERE r.room_id = ? ORDER BY r.ts`, roomID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r Reaction
		if err = rows.Scan(&r.EventID, &r.Target, &r.Sender, &r.Key, &r.Shortcode, &r.TargetBody); err != nil {
			return err
		}
		fn(r)
	}
	return rows.Err()
}

// RecentSenders returns up to n distinct senders of one room, most recent first.
func (s *Store) RecentSenders(ctx context.Context, roomID string, n int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT sender FROM messages WHERE room_id = ? GROUP BY sender ORDER BY MAX(ts) DESC LIMIT ?`, roomID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err = rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
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
	if _, err := s.db.ExecContext(ctx, `DELETE FROM reactions WHERE room_id = ?`, roomID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE room_id = ?`, roomID)
	return err
}
