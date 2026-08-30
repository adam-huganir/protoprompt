// Package db opens the SQLite database (modernc pure-Go driver) and applies schema.
package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Open opens (creating if needed) the SQLite file at path with WAL and
// foreign keys enabled. Tries driver DSN pragmas first; on failure falls back
// to per-connection pragmas with a single pooled connection.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err == nil {
		if err = db.Ping(); err == nil {
			if err = applySchema(db); err == nil {
				return db, nil
			}
		}
		db.Close()
	}
	// Fallback: plain path + per-connection pragmas, one pooled connection.
	db, err = sql.Open("sqlite", "file:"+path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := execPragmas(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if err := applySchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func execPragmas(db *sql.DB) error {
	for _, p := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=1"} {
		if _, err := db.Exec(p); err != nil {
			return err
		}
	}
	return nil
}

func applySchema(db *sql.DB) error {
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
	}
	return nil
}

// NowMS returns the current unix time in milliseconds.
func NowMS() int64 {
	return time.Now().UnixMilli()
}
