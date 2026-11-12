package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"

	"undined-go/internal/constants"
	"undined-go/internal/storage/maildir"
	"undined-go/internal/storage/parser"
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

	// 6. Seed known_addresses from existing messages if table is empty
	var addrCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM known_addresses;").Scan(&addrCount)
	if addrCount == 0 {
		if err := seedKnownAddresses(db); err != nil {
			log.Printf("[database] migration failed to seed known addresses: %v", err)
		}
	}

	// 7. Migrate legacy comments erroneously saved as 'submitted' to 'draft'
	_, _ = db.Exec("UPDATE patch_comments SET state = 'draft' WHERE state = 'submitted';")

	// 8. Reconcile replied status across folders from Sent messages and sanitize local-only folders
	if err := reconcileSentReplies(db); err != nil {
		log.Printf("[database] warning: failed to reconcile sent replies: %v", err)
	}
	if err := sanitizeLocalFolderMessages(db); err != nil {
		log.Printf("[database] warning: failed to sanitize local folder messages: %v", err)
	}

	return nil
}

func reconcileSentReplies(db *sql.DB) error {
	sentRows, err := db.Query(`
		SELECT m.in_reply_to
		FROM messages m
		JOIN folders f ON m.folder_id = f.id
		WHERE f.name = ? AND m.in_reply_to IS NOT NULL AND m.in_reply_to != '';
	`, constants.FolderSent)
	if err != nil {
		return err
	}

	repliedMsgIDs := make(map[string]bool)
	for sentRows.Next() {
		var inReplyTo sql.NullString
		if err := sentRows.Scan(&inReplyTo); err == nil && inReplyTo.String != "" {
			for _, id := range parser.ExtractMsgIDs(inReplyTo.String) {
				if id != "" {
					repliedMsgIDs[id] = true
				}
			}
		}
	}
	if err := sentRows.Err(); err != nil {
		_ = sentRows.Close()
		return err
	}
	if err := sentRows.Close(); err != nil {
		return err
	}

	if len(repliedMsgIDs) == 0 {
		return nil
	}

	msgRows, err := db.Query(`
		SELECT m.id, m.message_id, m.imap_uid, m.file_path, m.is_replied, m.flags
		FROM messages m
		JOIN folders f ON m.folder_id = f.id
		WHERE f.name NOT IN (?, ?);
	`, constants.FolderDrafts, constants.FolderSent)
	if err != nil {
		return err
	}

	type replyUpdate struct {
		id       int64
		filePath string
		flags    string
	}
	var updates []replyUpdate
	mdStore := maildir.NewMaildirStore("")

	for msgRows.Next() {
		var id int64
		var msgID string
		var uid sql.NullInt64
		var filePath, flags sql.NullString
		var isRepliedRaw any
		if err := msgRows.Scan(&id, &msgID, &uid, &filePath, &isRepliedRaw, &flags); err != nil {
			continue
		}

		cleanID := parser.CleanMsgID(msgID)
		if cleanID == "" || !repliedMsgIDs[cleanID] {
			continue
		}

		curReplied := toBool(isRepliedRaw)
		curPath := filePath.String
		newPath := curPath
		if curPath != "" && isMaildirFilePath(curPath) && !maildir.IsReplied(curPath) {
			if updatedPath, flagErr := mdStore.AddFlag(curPath, constants.FlagReplied); flagErr == nil && updatedPath != "" {
				newPath = updatedPath
			}
		}

		newFlags := ensureRepliedFlagToken(flags.String, uid.Valid)
		if !curReplied || newPath != curPath || newFlags != flags.String {
			updates = append(updates, replyUpdate{
				id:       id,
				filePath: newPath,
				flags:    newFlags,
			})
		}
	}
	if err := msgRows.Err(); err != nil {
		_ = msgRows.Close()
		return err
	}
	if err := msgRows.Close(); err != nil {
		return err
	}

	if len(updates) == 0 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("UPDATE messages SET is_replied = 1, file_path = ?, flags = ? WHERE id = ?;")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, u := range updates {
		if _, err := stmt.Exec(u.filePath, u.flags, u.id); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("[database] reconciled replied state on %d message(s) from Sent history", len(updates))
	return nil
}

func hasRepliedFlagToken(flags string) bool {
	for _, tok := range strings.Fields(flags) {
		if tok == `\Answered` || tok == "Answered" {
			return true
		}
		if !strings.HasPrefix(tok, `\`) && !strings.HasPrefix(tok, "$") && !strings.EqualFold(tok, "recent") && strings.Contains(tok, "R") {
			return true
		}
	}
	return false
}

func ensureRepliedFlagToken(flags string, isIMAP bool) string {
	if hasRepliedFlagToken(flags) {
		return flags
	}
	tokens := strings.Fields(flags)
	if isIMAP {
		tokens = append(tokens, `\Answered`)
	} else {
		tokens = append(tokens, constants.FlagReplied)
	}
	return strings.Join(tokens, " ")
}

func isMaildirFilePath(filePath string) bool {
	return strings.Contains(filePath, ":2,") ||
		strings.Contains(filePath, "/cur/") ||
		strings.Contains(filePath, "/new/")
}

func isStandardSystemFlagToken(tok string) bool {
	switch strings.ToLower(tok) {
	case `\seen`, "seen", `\answered`, "answered", `\flagged`, "flagged",
		`\deleted`, "deleted", `\draft`, "draft", `\recent`, "recent":
		return true
	}
	if tok == "" {
		return true
	}
	for _, r := range tok {
		switch r {
		case 'D', 'F', 'P', 'R', 'S', 'T':
		default:
			return false
		}
	}
	return true
}

func sanitizeLocalMessageFlags(currentFlags, filePath string) string {
	mdFlags := maildir.ExtractFlags(filePath)
	var out []string
	if mdFlags != "" {
		out = append(out, mdFlags)
	}
	for _, tok := range strings.Fields(currentFlags) {
		if !isStandardSystemFlagToken(tok) {
			out = append(out, tok)
		}
	}
	return strings.Join(out, " ")
}

func sanitizeLocalFolderMessages(db *sql.DB) error {
	rows, err := db.Query(`
		SELECT m.id, m.imap_uid, m.file_path, m.is_read, m.is_replied, m.flags
		FROM messages m
		JOIN folders f ON m.folder_id = f.id
		WHERE f.name IN (?, ?, ?, ?, ?, ?, ?);
	`,
		constants.FolderB4Imports,
		constants.FolderDrafts,
		constants.FolderTODO,
		constants.FolderSent,
		constants.FolderAIReviews,
		constants.FolderTrash,
		constants.FolderOutbox,
	)
	if err != nil {
		return err
	}

	type msgSanitizeUpdate struct {
		id        int64
		isRead    int
		isReplied int
		flags     string
	}
	var updates []msgSanitizeUpdate

	for rows.Next() {
		var id int64
		var uid sql.NullInt64
		var filePath, flags sql.NullString
		var isReadRaw, isRepliedRaw any
		if err := rows.Scan(&id, &uid, &filePath, &isReadRaw, &isRepliedRaw, &flags); err != nil {
			continue
		}

		curRead := toBool(isReadRaw)
		curReplied := toBool(isRepliedRaw)
		curFlags := flags.String
		hadIMAPUID := uid.Valid

		expectedRead := curRead
		expectedReplied := curReplied
		expectedFlags := curFlags

		if filePath.String != "" && (hadIMAPUID || isMaildirFilePath(filePath.String)) {
			expectedRead = maildir.IsRead(filePath.String)
			expectedReplied = maildir.IsReplied(filePath.String)
			expectedFlags = sanitizeLocalMessageFlags(curFlags, filePath.String)
		}

		if hadIMAPUID || expectedRead != curRead || expectedReplied != curReplied || expectedFlags != curFlags {
			readInt := 0
			if expectedRead {
				readInt = 1
			}
			repliedInt := 0
			if expectedReplied {
				repliedInt = 1
			}
			updates = append(updates, msgSanitizeUpdate{
				id:        id,
				isRead:    readInt,
				isReplied: repliedInt,
				flags:     expectedFlags,
			})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	// Check IMAP folders for stale cross-folder local Maildir paths that no longer exist on disk
	imapRows, err := db.Query(`
		SELECT m.id, m.file_path
		FROM messages m
		JOIN folders f ON m.folder_id = f.id
		WHERE f.name NOT IN (?, ?, ?, ?, ?, ?, ?)
		  AND m.imap_uid IS NOT NULL
		  AND m.file_path != '';
	`,
		constants.FolderB4Imports,
		constants.FolderDrafts,
		constants.FolderTODO,
		constants.FolderSent,
		constants.FolderAIReviews,
		constants.FolderTrash,
		constants.FolderOutbox,
	)
	if err != nil {
		return err
	}
	var staleFileIDs []int64
	for imapRows.Next() {
		var id int64
		var fp string
		if err := imapRows.Scan(&id, &fp); err == nil && fp != "" {
			if _, statErr := os.Stat(fp); os.IsNotExist(statErr) {
				staleFileIDs = append(staleFileIDs, id)
			}
		}
	}
	if err := imapRows.Err(); err != nil {
		_ = imapRows.Close()
		return err
	}
	if err := imapRows.Close(); err != nil {
		return err
	}

	if len(updates) == 0 && len(staleFileIDs) == 0 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if len(updates) > 0 {
		stmt, err := tx.Prepare("UPDATE messages SET imap_uid = NULL, is_read = ?, is_replied = ?, flags = ? WHERE id = ?;")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, u := range updates {
			if _, err := stmt.Exec(u.isRead, u.isReplied, u.flags, u.id); err != nil {
				return err
			}
		}
	}

	for _, id := range staleFileIDs {
		if _, err := tx.Exec("UPDATE messages SET file_path = '' WHERE id = ?;", id); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("[database] sanitized %d local folder message(s) and %d stale IMAP file path(s)", len(updates), len(staleFileIDs))
	return nil
}

func seedKnownAddresses(db *sql.DB) error {
	rows, err := db.Query("SELECT to_addr, cc_addr FROM messages WHERE (to_addr IS NOT NULL AND to_addr != '') OR (cc_addr IS NOT NULL AND cc_addr != '');")
	if err != nil {
		return err
	}
	defer rows.Close()

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
	INSERT INTO known_addresses (name, address, frequency, last_seen)
	VALUES (?, ?, 1, CURRENT_TIMESTAMP)
	ON CONFLICT(address) DO UPDATE SET
		frequency = known_addresses.frequency + 1,
		last_seen = CURRENT_TIMESTAMP,
		name = CASE WHEN excluded.name != '' THEN excluded.name ELSE known_addresses.name END;
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for rows.Next() {
		var toAddr, ccAddr sql.NullString
		if err := rows.Scan(&toAddr, &ccAddr); err != nil {
			continue
		}
		addrs := parser.ExtractAddresses(toAddr.String, ccAddr.String)
		for _, a := range addrs {
			_, _ = stmt.Exec(a.Name, a.Address)
		}
	}

	return tx.Commit()
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
