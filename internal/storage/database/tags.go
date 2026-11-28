package database

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"undined-go/internal/constants"
)

// IsLocalOnlyFolder reports whether folderName is an UndineD local-only mailbox
// (not backed by a remote IMAP folder).
func IsLocalOnlyFolder(folderName string) bool {
	switch folderName {
	case constants.FolderB4Imports,
		constants.FolderDrafts,
		constants.FolderTODO,
		constants.FolderOutbox,
		constants.FolderSent,
		constants.FolderAIReviews,
		constants.FolderTrash:
		return true
	default:
		return false
	}
}

// ExtractUserTagsFromFlags parses a space/comma-separated flags column value and
// returns all non-system user keyword tags sorted by priority rank (never nil).
func ExtractUserTagsFromFlags(flags string) []string {
	tags := make([]string, 0)
	if strings.TrimSpace(flags) == "" {
		return tags
	}
	cleaned := strings.ReplaceAll(flags, ",", " ")
	seen := make(map[string]struct{})
	for _, tok := range strings.Fields(cleaned) {
		if tok == "" || strings.HasPrefix(tok, `\`) || isStandardSystemFlagToken(tok) {
			continue
		}
		lower := strings.ToLower(tok)
		if _, dup := seen[lower]; dup {
			continue
		}
		seen[lower] = struct{}{}
		tags = append(tags, tok)
	}
	sort.SliceStable(tags, func(i, j int) bool {
		ri := flagTokenPriorityRank(tags[i])
		rj := flagTokenPriorityRank(tags[j])
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(tags[i]) < strings.ToLower(tags[j])
	})
	return tags
}

// ExtractSystemFlagsFromFlags returns all IMAP/Maildir system flag tokens from flags,
// preserving order and deduplicating case-insensitively (never nil).
func ExtractSystemFlagsFromFlags(flags string) []string {
	sys := make([]string, 0)
	if strings.TrimSpace(flags) == "" {
		return sys
	}
	cleaned := strings.ReplaceAll(flags, ",", " ")
	seen := make(map[string]struct{})
	for _, tok := range strings.Fields(cleaned) {
		if tok == "" {
			continue
		}
		if strings.HasPrefix(tok, `\`) || isStandardSystemFlagToken(tok) {
			lower := strings.ToLower(tok)
			if _, dup := seen[lower]; dup {
				continue
			}
			seen[lower] = struct{}{}
			sys = append(sys, tok)
		}
	}
	return sys
}

func syncEmailTagsTx(tx *sql.Tx, emailID int64, tags []string, nowUnix int64) error {
	if _, err := tx.Exec("DELETE FROM email_tags WHERE email_id = ?;", emailID); err != nil {
		return fmt.Errorf("clear email_tags for %d: %w", emailID, err)
	}
	if len(tags) == 0 {
		return nil
	}
	stmt, err := tx.Prepare("INSERT OR REPLACE INTO email_tags (email_id, tag, updated_at) VALUES (?, ?, ?);")
	if err != nil {
		return fmt.Errorf("prepare email_tags insert: %w", err)
	}
	defer stmt.Close()

	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		t := strings.TrimSpace(tag)
		if t == "" {
			continue
		}
		lower := strings.ToLower(t)
		if _, dup := seen[lower]; dup {
			continue
		}
		seen[lower] = struct{}{}
		if _, err := stmt.Exec(emailID, t, nowUnix); err != nil {
			return fmt.Errorf("insert email_tag %q for %d: %w", t, emailID, err)
		}
	}
	return nil
}

func syncEmailTagsDB(db *sql.DB, emailID int64, flags string) error {
	tags := ExtractUserTagsFromFlags(flags)
	now := time.Now().Unix()

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := syncEmailTagsTx(tx, emailID, tags, now); err != nil {
		return err
	}
	return tx.Commit()
}

func syncAllEmailTagsFromFlags(db *sql.DB) error {
	rows, err := db.Query("SELECT id, COALESCE(flags, '') FROM messages;")
	if err != nil {
		return err
	}
	defer rows.Close()

	type msgTags struct {
		id   int64
		tags []string
	}
	var items []msgTags
	for rows.Next() {
		var id int64
		var flags string
		if err := rows.Scan(&id, &flags); err != nil {
			return fmt.Errorf("scan message flags for email_tags sync: %w", err)
		}
		items = append(items, msgTags{
			id:   id,
			tags: ExtractUserTagsFromFlags(flags),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM email_tags;"); err != nil {
		return err
	}

	stmt, err := tx.Prepare("INSERT OR IGNORE INTO email_tags (email_id, tag, updated_at) VALUES (?, ?, ?);")
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().Unix()
	for _, item := range items {
		for _, tag := range item.tags {
			if _, err := stmt.Exec(item.id, tag, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// TagUpdateOp describes an atomic tag mutation for a single message copy.
type TagUpdateOp struct {
	EmailID         int64
	FolderID        int64
	FolderName      string
	IMAPUID         uint32
	NewFlags        string
	AfterTags       []string
	AddTags         []string
	RemoveTags      []string
	EnqueueIMAPSync bool
}

// BatchUpdateEmailTags applies all provided tag updates inside a single SQLite
// transaction, updating messages.flags, email_tags, and conditionally enqueueing
// remote IMAP flag synchronization jobs in imap_sync_queue.
func (d *Database) BatchUpdateEmailTags(ops []TagUpdateOp) (int, error) {
	if len(ops) == 0 {
		return 0, nil
	}

	tx, err := d.DB.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tag update transaction: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	queuedCount := 0

	for _, op := range ops {
		if _, err := tx.Exec("UPDATE messages SET flags = ? WHERE id = ?;", op.NewFlags, op.EmailID); err != nil {
			return 0, fmt.Errorf("update flags for message %d: %w", op.EmailID, err)
		}
		if err := syncEmailTagsTx(tx, op.EmailID, op.AfterTags, now); err != nil {
			return 0, err
		}
		if op.EnqueueIMAPSync && op.IMAPUID > 0 && !IsLocalOnlyFolder(op.FolderName) && (len(op.AddTags) > 0 || len(op.RemoveTags) > 0) {
			if _, err := tx.Exec(`
				INSERT INTO imap_sync_queue (
					email_id, folder_id, folder_name, imap_uid,
					add_tags, remove_tags, status, attempts, last_error, created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, 'pending', 0, '', ?, ?);
			`,
				op.EmailID,
				op.FolderID,
				op.FolderName,
				op.IMAPUID,
				strings.Join(op.AddTags, " "),
				strings.Join(op.RemoveTags, " "),
				now,
				now,
			); err != nil {
				return 0, fmt.Errorf("enqueue imap tag sync for message %d: %w", op.EmailID, err)
			}
			queuedCount++
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit tag update transaction: %w", err)
	}
	d.MarkInProcessWrite()
	return queuedCount, nil
}

// GetTagCounts returns the message count per tag stored in email_tags,
// optionally restricted to a specific folder name.
func (d *Database) GetTagCounts(folderName string) (map[string]int, error) {
	counts := make(map[string]int)
	var rows *sql.Rows
	var err error
	if strings.TrimSpace(folderName) != "" {
		rows, err = d.DB.Query(`
			SELECT et.tag, COUNT(*)
			FROM email_tags et
			JOIN messages m ON et.email_id = m.id
			JOIN folders f ON m.folder_id = f.id
			WHERE f.name = ?
			GROUP BY et.tag;
		`, folderName)
	} else {
		rows, err = d.DB.Query(`
			SELECT tag, COUNT(*)
			FROM email_tags
			GROUP BY tag;
		`)
	}
	if err != nil {
		return counts, err
	}
	defer rows.Close()

	for rows.Next() {
		var tag string
		var cnt int
		if err := rows.Scan(&tag, &cnt); err != nil {
			return counts, err
		}
		counts[tag] = cnt
	}
	return counts, rows.Err()
}

// IMAPSyncQueueItem represents a queued remote IMAP keyword flag sync operation.
type IMAPSyncQueueItem struct {
	ID         int64
	EmailID    int64
	FolderID   int64
	FolderName string
	IMAPUID    uint32
	AddTags    []string
	RemoveTags []string
	Status     string
	Attempts   int
	LastError  string
	CreatedAt  int64
	UpdatedAt  int64
}

// ListPendingIMAPSyncQueue returns pending tag sync jobs ordered FIFO.
func (d *Database) ListPendingIMAPSyncQueue(limit int) ([]*IMAPSyncQueueItem, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.DB.Query(`
		SELECT id, email_id, folder_id, folder_name, imap_uid,
		       add_tags, remove_tags, status, attempts, last_error, created_at, updated_at
		FROM imap_sync_queue
		WHERE status = 'pending'
		ORDER BY id ASC
		LIMIT ?;
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]*IMAPSyncQueueItem, 0)
	for rows.Next() {
		var item IMAPSyncQueueItem
		var uid int64
		var addStr, removeStr string
		if err := rows.Scan(
			&item.ID, &item.EmailID, &item.FolderID, &item.FolderName, &uid,
			&addStr, &removeStr, &item.Status, &item.Attempts, &item.LastError,
			&item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		item.IMAPUID = uint32(uid)
		item.AddTags = strings.Fields(addStr)
		item.RemoveTags = strings.Fields(removeStr)
		items = append(items, &item)
	}
	return items, rows.Err()
}

// MarkIMAPSyncQueueDone marks a queued IMAP tag sync job as completed.
func (d *Database) MarkIMAPSyncQueueDone(id int64) error {
	_, err := d.DB.Exec(
		"UPDATE imap_sync_queue SET status = 'done', last_error = '', updated_at = ? WHERE id = ?;",
		time.Now().Unix(), id,
	)
	if err == nil {
		d.MarkInProcessWrite()
	}
	return err
}

// MarkIMAPSyncQueueFailed increments attempts and records an error on a pending queued job,
// transitioning it to 'failed' once attempts reach maxAttempts.
func (d *Database) MarkIMAPSyncQueueFailed(id int64, errMsg string, maxAttempts int) error {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	_, err := d.DB.Exec(`
		UPDATE imap_sync_queue
		SET attempts = attempts + 1,
		    last_error = ?,
		    status = CASE WHEN attempts + 1 >= ? THEN 'failed' ELSE 'pending' END,
		    updated_at = ?
		WHERE id = ? AND status = 'pending';
	`, errMsg, maxAttempts, time.Now().Unix(), id)
	if err == nil {
		d.MarkInProcessWrite()
	}
	return err
}

// CountPendingIMAPSyncQueue returns the number of pending IMAP tag sync items.
func (d *Database) CountPendingIMAPSyncQueue() (int, error) {
	var cnt int
	err := d.DB.QueryRow("SELECT COUNT(*) FROM imap_sync_queue WHERE status = 'pending';").Scan(&cnt)
	return cnt, err
}

// MessageFilterOptions specifies filter parameters for ListMessagesFiltered.
type MessageFilterOptions struct {
	FolderID    *int64
	Limit       int
	Offset      int
	ExcludeFrom []string
	HasTags     []string
	WithoutTags []string
	IsPatch     *bool
}

func makePlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ", ")
}

// ListMessagesFiltered lists messages using SQLite filters backed by email_tags.
func (d *Database) ListMessagesFiltered(opts MessageFilterOptions) ([]*Message, error) {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("SELECT %s FROM messages m JOIN folders f ON m.folder_id = f.id WHERE 1=1 ", messageSelectColumns))

	var args []any
	if opts.FolderID != nil {
		sb.WriteString("AND m.folder_id = ? ")
		args = append(args, *opts.FolderID)
	}
	if opts.IsPatch != nil {
		if *opts.IsPatch {
			sb.WriteString("AND m.is_patch = 1 ")
		} else {
			sb.WriteString("AND m.is_patch = 0 ")
		}
	}
	if len(opts.ExcludeFrom) > 0 {
		sb.WriteString(fmt.Sprintf("AND LOWER(COALESCE(m.from_addr, '')) NOT IN (%s) ", makePlaceholders(len(opts.ExcludeFrom))))
		for _, addr := range opts.ExcludeFrom {
			args = append(args, strings.ToLower(strings.TrimSpace(addr)))
		}
	}
	if len(opts.HasTags) > 0 {
		sb.WriteString(fmt.Sprintf("AND EXISTS (SELECT 1 FROM email_tags t WHERE t.email_id = m.id AND LOWER(t.tag) IN (%s)) ", makePlaceholders(len(opts.HasTags))))
		for _, tag := range opts.HasTags {
			args = append(args, strings.ToLower(strings.TrimSpace(tag)))
		}
	}
	if len(opts.WithoutTags) > 0 {
		sb.WriteString(fmt.Sprintf("AND NOT EXISTS (SELECT 1 FROM email_tags t WHERE t.email_id = m.id AND LOWER(t.tag) IN (%s)) ", makePlaceholders(len(opts.WithoutTags))))
		for _, tag := range opts.WithoutTags {
			args = append(args, strings.ToLower(strings.TrimSpace(tag)))
		}
	}

	sb.WriteString("ORDER BY m.epoch_timestamp DESC, m.id DESC ")
	if opts.Limit > 0 {
		sb.WriteString("LIMIT ? ")
		args = append(args, opts.Limit)
	} else if opts.Offset > 0 {
		sb.WriteString("LIMIT -1 ")
	}
	if opts.Offset > 0 {
		sb.WriteString("OFFSET ? ")
		args = append(args, opts.Offset)
	}

	rows, err := d.DB.Query(sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list filtered messages: %w", err)
	}
	defer rows.Close()

	messages := make([]*Message, 0)
	for rows.Next() {
		m, scanErr := scanMessage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// SearchOptions specifies optional folder and tag filters for SearchWithOptions.
type SearchOptions struct {
	Limit       int
	FolderName  string
	HasTags     []string
	WithoutTags []string
}

// SearchWithOptions performs FTS5 search with optional folder and email_tags filters.
func (d *Database) SearchWithOptions(query string, opts SearchOptions) ([]*SearchResult, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}

	cleanQuery := strings.TrimSpace(query)
	if cleanQuery == "" {
		return []*SearchResult{}, nil
	}

	buildSQL := func() (string, []any) {
		var sb strings.Builder
		sb.WriteString(`
		SELECT
			m.id,
			m.message_id,
			f.name AS folder_name,
			m.subject,
			COALESCE(m.from_name, ''),
			m.from_addr,
			m.date,
			m.is_patch,
			m.is_read,
			m.is_replied,
			COALESCE(m.flags, ''),
			COALESCE(m.in_reply_to, ''),
			COALESCE(m.references_header, ''),
			snippet(messages_fts, 3, '<mark>', '</mark>', '...', 32) AS matched_snippet
		FROM messages_fts
		JOIN messages m ON messages_fts.rowid = m.id
		JOIN folders f ON m.folder_id = f.id
		WHERE messages_fts MATCH ? `)

		args := []any{cleanQuery}
		if strings.TrimSpace(opts.FolderName) != "" {
			sb.WriteString("AND f.name = ? ")
			args = append(args, strings.TrimSpace(opts.FolderName))
		}
		if len(opts.HasTags) > 0 {
			sb.WriteString(fmt.Sprintf("AND EXISTS (SELECT 1 FROM email_tags t WHERE t.email_id = m.id AND LOWER(t.tag) IN (%s)) ", makePlaceholders(len(opts.HasTags))))
			for _, tag := range opts.HasTags {
				args = append(args, strings.ToLower(strings.TrimSpace(tag)))
			}
		}
		if len(opts.WithoutTags) > 0 {
			sb.WriteString(fmt.Sprintf("AND NOT EXISTS (SELECT 1 FROM email_tags t WHERE t.email_id = m.id AND LOWER(t.tag) IN (%s)) ", makePlaceholders(len(opts.WithoutTags))))
			for _, tag := range opts.WithoutTags {
				args = append(args, strings.ToLower(strings.TrimSpace(tag)))
			}
		}
		sb.WriteString("ORDER BY rank LIMIT ?;")
		args = append(args, limit)
		return sb.String(), args
	}

	sqlQuery, args := buildSQL()
	rows, err := d.DB.Query(sqlQuery, args...)
	if err != nil {
		escaped := fmt.Sprintf(`"%s"`, strings.ReplaceAll(cleanQuery, `"`, `""`))
		args[0] = escaped
		rows, err = d.DB.Query(sqlQuery, args...)
		if err != nil {
			return nil, fmt.Errorf("FTS5 search error: %w", err)
		}
	}
	defer rows.Close()

	results := make([]*SearchResult, 0)
	for rows.Next() {
		var r SearchResult
		var isPatch, isRead, isReplied any
		if err := rows.Scan(
			&r.ID,
			&r.MessageID,
			&r.FolderName,
			&r.Subject,
			&r.FromName,
			&r.FromAddr,
			&r.Date,
			&isPatch,
			&isRead,
			&isReplied,
			&r.Flags,
			&r.InReplyTo,
			&r.ReferencesHeader,
			&r.Snippet,
		); err != nil {
			return nil, err
		}
		r.IsPatch = toBool(isPatch)
		r.IsRead = toBool(isRead)
		r.IsReplied = toBool(isReplied)
		r.Tags = []MessageTag{}
		results = append(results, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// ThreadMessageMeta is a lightweight projection of a message row without body_plain,
// used to aggregate threads quickly without loading large patch diffs into memory.
type ThreadMessageMeta struct {
	ID               int64
	MessageID        string
	FolderID         int64
	FolderName       string
	FilePath         string
	Subject          string
	FromName         string
	FromAddr         string
	Date             string
	EpochTimestamp   int64
	InReplyTo        string
	ReferencesHeader string
	IsPatch          bool
	IsReplied        bool
	Flags            string
	HasBodyPlain     bool
}

// ListAllMessagesMeta returns lightweight metadata for all non-AI-review messages across folders.
func (d *Database) ListAllMessagesMeta() ([]*ThreadMessageMeta, error) {
	rows, err := d.DB.Query(`
		SELECT
			m.id,
			COALESCE(m.message_id, ''),
			m.folder_id,
			f.name,
			COALESCE(m.file_path, ''),
			COALESCE(m.subject, ''),
			COALESCE(m.from_name, ''),
			COALESCE(m.from_addr, ''),
			COALESCE(m.date, ''),
			COALESCE(m.epoch_timestamp, 0),
			COALESCE(m.in_reply_to, ''),
			COALESCE(m.references_header, ''),
			m.is_patch,
			m.is_replied,
			COALESCE(m.flags, ''),
			CASE WHEN m.body_plain IS NOT NULL AND m.body_plain != '' THEN 1 ELSE 0 END AS has_body
		FROM messages m
		JOIN folders f ON m.folder_id = f.id
		WHERE f.name != ?
		ORDER BY m.epoch_timestamp ASC, m.id ASC;
	`, constants.FolderAIReviews)
	if err != nil {
		return nil, fmt.Errorf("list thread message metadata: %w", err)
	}
	defer rows.Close()

	res := make([]*ThreadMessageMeta, 0)
	for rows.Next() {
		var m ThreadMessageMeta
		var isPatch, isReplied, hasBody any
		if err := rows.Scan(
			&m.ID, &m.MessageID, &m.FolderID, &m.FolderName, &m.FilePath,
			&m.Subject, &m.FromName, &m.FromAddr, &m.Date, &m.EpochTimestamp,
			&m.InReplyTo, &m.ReferencesHeader, &isPatch, &isReplied, &m.Flags, &hasBody,
		); err != nil {
			return nil, err
		}
		m.IsPatch = toBool(isPatch)
		m.IsReplied = toBool(isReplied)
		m.HasBodyPlain = toBool(hasBody)
		res = append(res, &m)
	}
	return res, rows.Err()
}
