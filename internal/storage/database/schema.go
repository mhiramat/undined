// Package database manages SQLite storage, safe migrations, and FTS5 indexing.
package database

const SchemaSQL = `
CREATE TABLE IF NOT EXISTS folders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    last_synced_uid INTEGER DEFAULT 0,
    sync_enabled BOOLEAN DEFAULT 1
);

CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT NOT NULL,
    folder_id INTEGER NOT NULL REFERENCES folders(id),
    imap_uid INTEGER,
    file_path TEXT NOT NULL,
    subject TEXT,
    from_name TEXT,
    from_addr TEXT,
    to_addr TEXT,
    cc_addr TEXT,
    date TEXT,
    epoch_timestamp INTEGER,
    in_reply_to TEXT,
    references_header TEXT,
    body_plain TEXT,
    is_patch BOOLEAN DEFAULT 0,
    is_read BOOLEAN DEFAULT 0,
    is_replied BOOLEAN DEFAULT 0,
    has_notes BOOLEAN DEFAULT 0,
    flags TEXT DEFAULT '',
    review_commit_id TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(folder_id, message_id)
);

CREATE TABLE IF NOT EXISTS review_commits (
    message_id TEXT PRIMARY KEY,
    commit_id TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS patch_comments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT NOT NULL,
    message_db_id INTEGER REFERENCES messages(id) ON DELETE CASCADE,
    file_path TEXT NOT NULL,
    line_no INTEGER NOT NULL,
    side TEXT DEFAULT 'new',
    text TEXT NOT NULL,
    state TEXT DEFAULT 'draft',
    author TEXT DEFAULT 'user',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_messages_in_reply_to ON messages(in_reply_to);
CREATE INDEX IF NOT EXISTS idx_messages_folder ON messages(folder_id);
CREATE INDEX IF NOT EXISTS idx_messages_epoch ON messages(epoch_timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_messages_msgid ON messages(message_id);
CREATE INDEX IF NOT EXISTS idx_messages_folder_msgid ON messages(folder_id, message_id);
CREATE INDEX IF NOT EXISTS idx_messages_imap_uid ON messages(folder_id, imap_uid);
CREATE INDEX IF NOT EXISTS idx_messages_flags ON messages(flags);
CREATE INDEX IF NOT EXISTS idx_messages_is_replied ON messages(is_replied);
CREATE INDEX IF NOT EXISTS idx_messages_review_commit_id ON messages(review_commit_id);
CREATE INDEX IF NOT EXISTS idx_messages_has_notes ON messages(has_notes);

CREATE INDEX IF NOT EXISTS idx_patch_comments_msgid ON patch_comments(message_id);
CREATE INDEX IF NOT EXISTS idx_patch_comments_msg_db_id ON patch_comments(message_db_id);
CREATE INDEX IF NOT EXISTS idx_patch_comments_state ON patch_comments(state);
CREATE INDEX IF NOT EXISTS idx_patch_comments_file_line ON patch_comments(message_id, file_path, line_no);

CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    message_id UNINDEXED,
    subject,
    from_addr,
    body_plain,
    content='messages',
    content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, message_id, subject, from_addr, body_plain)
  VALUES (new.id, new.message_id, new.subject, new.from_addr, new.body_plain);
END;

CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, message_id, subject, from_addr, body_plain)
  VALUES('delete', old.id, old.message_id, old.subject, old.from_addr, old.body_plain);
END;

CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, message_id, subject, from_addr, body_plain)
  VALUES('delete', old.id, old.message_id, old.subject, old.from_addr, old.body_plain);
  INSERT INTO messages_fts(rowid, message_id, subject, from_addr, body_plain)
  VALUES (new.id, new.message_id, new.subject, new.from_addr, new.body_plain);
END;
`
