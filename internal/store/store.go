// Package store holds typed access to prompt revisions, sessions, and the
// message tree.
package store

import "database/sql"

type Store struct {
	Dir string // prompt workspace directory (absolute or cwd-relative)
	DB  *sql.DB
}

func New(dir string, db *sql.DB) *Store {
	return &Store{Dir: dir, DB: db}
}
