package database

import (
	"database/sql"
	"fmt"
	"strings"

	"undined-go/internal/constants"
)

// initDB runs safe schema migrations, creates tables and indexes, and seeds standard folders.
func initDB(db *sql.DB) error {
	// 1. Safe Migration Ordering: If messages table exists, check and add missing columns BEFORE SchemaSQL
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='messages';").Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to check messages table: %w", err)
	}

	if count > 0 {
		// Existing messages table: inspect columns
		cols, err := getTableColumns(db, "messages")
		if err != nil {
			return fmt.Errorf("failed to get messages table columns: %w", err)
		}

		migrationColumns := []struct {
			name string
			ddl  string
		}{
			{"imap_uid", "ALTER TABLE messages ADD COLUMN imap_uid INTEGER;"},
			{"flags", "ALTER TABLE messages ADD COLUMN flags TEXT DEFAULT '';"},
			{"review_commit_id", "ALTER TABLE messages ADD COLUMN review_commit_id TEXT;"},
			{"has_notes", "ALTER TABLE messages ADD COLUMN has_notes BOOLEAN DEFAULT 0;"},
			{"is_replied", "ALTER TABLE messages ADD COLUMN is_replied BOOLEAN DEFAULT 0;"},
		}

		for _, col := range migrationColumns {
			if _, exists := cols[col.name]; !exists {
				if _, err := db.Exec(col.ddl); err != nil && !strings.Contains(err.Error(), "duplicate column") {
					return fmt.Errorf("migration failed for column %s: %w", col.name, err)
				}
			}
		}

		// 2. Check for legacy single-column UNIQUE(message_id) constraint
		hasLegacyUnique, err := hasSingleColumnUniqueMsgID(db)
		if err != nil {
			return fmt.Errorf("failed to inspect unique constraints: %w", err)
		}

		if hasLegacyUnique {
			if err := migrateToCompositeUnique(db); err != nil {
				return fmt.Errorf("failed to migrate to composite unique constraint: %w", err)
			}
		}
	}

	// 3. Execute SchemaSQL (creates tables, indexes, FTS5 virtual table, triggers)
	if _, err := db.Exec(SchemaSQL); err != nil {
		return fmt.Errorf("failed to execute schema SQL: %w", err)
	}

	// Ensure messages_fts is synchronized if legacy or existing messages exist
	_, _ = db.Exec("INSERT INTO messages_fts(messages_fts) VALUES('rebuild');")

	// 4. Seed standard mailboxes
	for _, folderName := range constants.StandardFolders {
		_, err := db.Exec(
			"INSERT OR IGNORE INTO folders (name, last_synced_uid, sync_enabled) VALUES (?, 0, ?);",
			folderName,
			folderName != constants.FolderDrafts && folderName != constants.FolderTrash && folderName != constants.FolderAIReviews,
		)
		if err != nil {
			return fmt.Errorf("failed to seed folder %s: %w", folderName, err)
		}
	}

	// 5. Migrate deprecated Outbox to TODO if present
	var outboxID, todoID sql.NullInt64
	_ = db.QueryRow("SELECT id FROM folders WHERE name = ?;", constants.FolderOutbox).Scan(&outboxID)
	_ = db.QueryRow("SELECT id FROM folders WHERE name = ?;", constants.FolderTODO).Scan(&todoID)
	if outboxID.Valid && todoID.Valid {
		_, _ = db.Exec("UPDATE messages SET folder_id = ? WHERE folder_id = ?;", todoID.Int64, outboxID.Int64)
		_, _ = db.Exec("DELETE FROM folders WHERE id = ?;", outboxID.Int64)
	} else if outboxID.Valid && !todoID.Valid {
		_, _ = db.Exec("UPDATE folders SET name = ? WHERE id = ?;", constants.FolderTODO, outboxID.Int64)
	}

	return nil
}

func getTableColumns(db *sql.DB, tableName string) (map[string]struct{}, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s);", tableName))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := make(map[string]struct{})
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue *string
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		cols[name] = struct{}{}
	}
	return cols, nil
}

func hasSingleColumnUniqueMsgID(db *sql.DB) (bool, error) {
	rows, err := db.Query("PRAGMA index_list('messages');")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var seq int
		var name string
		var unique int
		var origin, partial string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return false, err
		}
		if unique == 1 {
			infoRows, err := db.Query(fmt.Sprintf("PRAGMA index_info('%s');", name))
			if err != nil {
				continue
			}
			var indexedCols []string
			for infoRows.Next() {
				var seqno, cid int
				var colName string
				if err := infoRows.Scan(&seqno, &cid, &colName); err == nil {
					indexedCols = append(indexedCols, colName)
				}
			}
			infoRows.Close()

			if len(indexedCols) == 1 && indexedCols[0] == "message_id" {
				return true, nil
			}
		}
	}
	return false, nil
}

func migrateToCompositeUnique(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		"PRAGMA foreign_keys = OFF;",
		`CREATE TABLE messages_new (
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
		);`,
		`INSERT INTO messages_new (
			id, message_id, folder_id, imap_uid, file_path, subject, from_name, from_addr,
			to_addr, cc_addr, date, epoch_timestamp, in_reply_to, references_header,
			body_plain, is_patch, is_read, is_replied, has_notes, flags, review_commit_id, created_at
		)
		SELECT
			id, message_id, folder_id, imap_uid, file_path, subject, from_name, from_addr,
			to_addr, cc_addr, date, epoch_timestamp, in_reply_to, references_header,
			body_plain, is_patch, is_read, is_replied, has_notes, flags, review_commit_id, created_at
		FROM messages;`,
		"DROP TRIGGER IF EXISTS messages_ai;",
		"DROP TRIGGER IF EXISTS messages_ad;",
		"DROP TRIGGER IF EXISTS messages_au;",
		"DROP TABLE messages;",
		"ALTER TABLE messages_new RENAME TO messages;",
		"PRAGMA foreign_keys = ON;",
	}

	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("failed executing %s: %w", stmt, err)
		}
	}

	return tx.Commit()
}
