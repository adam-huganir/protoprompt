package db

// schema is applied verbatim, one statement at a time. session/message
// cross-reference each other; SQLite does not validate FK targets at CREATE
// TABLE time, so the order is fine.
var schema = []string{
	`CREATE TABLE IF NOT EXISTS prompt_rev (
		id        TEXT PRIMARY KEY,
		file      TEXT NOT NULL,
		text      TEXT NOT NULL,
		saved_at  INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_rev_file ON prompt_rev (file, saved_at)`,
	`CREATE TABLE IF NOT EXISTS message (
		id               TEXT PRIMARY KEY,
		session_id       TEXT NOT NULL REFERENCES session(id) ON DELETE CASCADE,
		parent_id        TEXT REFERENCES message(id) ON DELETE CASCADE,
		role             TEXT NOT NULL,
		text             TEXT NOT NULL DEFAULT '',
		reasoning_text   TEXT NOT NULL DEFAULT '',
		status           TEXT NOT NULL,
		preview          INTEGER NOT NULL DEFAULT 0,
		prompt_rev_id    TEXT REFERENCES prompt_rev(id) ON DELETE SET NULL,
		model            TEXT NOT NULL DEFAULT '',
		reasoning_effort TEXT NOT NULL DEFAULT '',
		temperature      REAL NOT NULL DEFAULT 1,
		max_tokens       INTEGER,
		error            TEXT NOT NULL DEFAULT '',
		prompt_tokens    INTEGER,
		output_tokens    INTEGER,
		latency_ms       INTEGER,
		created_at       INTEGER NOT NULL,
		finished_at      INTEGER
	)`,
	`CREATE INDEX IF NOT EXISTS idx_msg_session ON message (session_id, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_msg_parent ON message (parent_id)`,
	`CREATE TABLE IF NOT EXISTS session (
		id                TEXT PRIMARY KEY,
		file              TEXT NOT NULL,
		title             TEXT NOT NULL DEFAULT '',
		model             TEXT NOT NULL,
		reasoning_effort  TEXT NOT NULL DEFAULT '',
		temperature       REAL NOT NULL DEFAULT 1,
		max_tokens        INTEGER,
		extra_json        TEXT NOT NULL DEFAULT '{}',
		head_message_id   TEXT REFERENCES message(id) ON DELETE SET NULL,
		created_at        INTEGER NOT NULL,
		updated_at        INTEGER NOT NULL
	)`,
}
