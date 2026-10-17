package database

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	"undined-go/internal/storage/maildir"
	"undined-go/internal/storage/parser"
)

// Message represents an indexed email row from the SQLite messages table.
type Message struct {
	ID               int64   `json:"id"`
	MessageID        string  `json:"message_id"`
	FolderID         int64   `json:"folder_id"`
	FolderName       string  `json:"folder_name,omitempty"`
	IMAPUID          *uint32 `json:"imap_uid,omitempty"`
	FilePath         string  `json:"file_path"`
	Subject          string  `json:"subject"`
	FromName         string  `json:"from_name"`
	FromAddr         string  `json:"from_addr"`
	ToAddr           string  `json:"to_addr"`
	CcAddr           string  `json:"cc_addr"`
	Date             string  `json:"date"`
	EpochTimestamp   int64   `json:"epoch_timestamp"`
	InReplyTo        string  `json:"in_reply_to"`
	ReferencesHeader string  `json:"references_header"`
	BodyPlain        string  `json:"body_plain,omitempty"`
	IsPatch          bool    `json:"is_patch"`
	IsRead           bool    `json:"is_read"`
	IsReplied        bool    `json:"is_replied"`
	HasNotes         bool    `json:"has_notes"`
	Flags                string  `json:"flags"`
	ReviewCommitID       string  `json:"review_commit_id,omitempty"`
	ReviewCommitIDHyphen string  `json:"review-commit-id,omitempty"`
	CreatedAt            string  `json:"created_at"`

	TagKey   *string `json:"tag_key"`
	TagName  *string `json:"tag_name"`
	TagColor *string `json:"tag_color"`

	ThreadDepth  int    `json:"thread_depth"`
	ThreadPrefix string `json:"thread_prefix,omitempty"`
	HasChildren  bool   `json:"has_children"`
	IsAIReview   bool   `json:"is_ai_review,omitempty"`
}

// SearchResult represents full-text search match from FTS5.
type SearchResult struct {
	ID        int64  `json:"id"`
	MessageID string `json:"message_id"`
	Subject   string `json:"subject"`
	FromAddr  string `json:"from_addr"`
	Date      string `json:"date"`
	IsPatch   bool   `json:"is_patch"`
	Snippet   string `json:"snippet"`
}

// IndexEmailFile reads a raw .eml file from disk, parses it, and indexes it into the messages table.
func (d *Database) IndexEmailFile(folderID int64, filePath string, imapUID ...uint32) (int64, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return 0, fmt.Errorf("failed to read email file %s: %w", filePath, err)
	}

	parsed, err := parser.ParseEmail(data)
	if err != nil {
		return 0, fmt.Errorf("failed to parse email file %s: %w", filePath, err)
	}

	var uidVal *uint32
	if len(imapUID) > 0 && imapUID[0] > 0 {
		uidVal = &imapUID[0]
	}

	flags := maildir.ExtractFlags(filePath)
	isRead := maildir.IsRead(filePath)
	isReplied := maildir.IsReplied(filePath)

	msgID := parser.CleanMsgID(parsed.MessageID)
	if msgID == "" {
		// Fallback for draft or synthesized emails without Message-ID
		msgID = fmt.Sprintf("undine-%d-%d@local", folderID, parsed.EpochTimestamp)
	}

	query := `
	INSERT INTO messages (
		message_id, folder_id, imap_uid, file_path, subject, from_name, from_addr,
		to_addr, cc_addr, date, epoch_timestamp, in_reply_to, references_header,
		body_plain, is_patch, is_read, is_replied, has_notes, flags
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(folder_id, message_id) DO UPDATE SET
		file_path = excluded.file_path,
		imap_uid = COALESCE(excluded.imap_uid, messages.imap_uid),
		subject = excluded.subject,
		from_name = excluded.from_name,
		from_addr = excluded.from_addr,
		to_addr = excluded.to_addr,
		cc_addr = excluded.cc_addr,
		date = excluded.date,
		epoch_timestamp = excluded.epoch_timestamp,
		in_reply_to = excluded.in_reply_to,
		references_header = excluded.references_header,
		body_plain = excluded.body_plain,
		is_patch = excluded.is_patch,
		is_read = excluded.is_read,
		is_replied = excluded.is_replied,
		flags = excluded.flags;
	`

	res, err := d.DB.Exec(
		query,
		msgID,
		folderID,
		uidVal,
		filePath,
		parsed.Subject,
		parsed.FromName,
		parsed.FromAddr,
		parsed.ToAddr,
		parsed.CcAddr,
		parsed.Date,
		parsed.EpochTimestamp,
		parsed.InReplyTo,
		parsed.ReferencesHeader,
		parsed.BodyPlain,
		parsed.IsPatch,
		isRead,
		isReplied,
		false,
		flags,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert/update message in db: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil || id == 0 {
		// If ON CONFLICT updated an existing row, fetch its ID
		var existingID int64
		err2 := d.DB.QueryRow(
			"SELECT id FROM messages WHERE folder_id = ? AND message_id = ?;",
			folderID, msgID,
		).Scan(&existingID)
		if err2 == nil {
			return existingID, nil
		}
	}
	return id, nil
}

func scanMessage(row interface {
	Scan(dest ...any) error
}) (*Message, error) {
	var m Message
	var uid sql.NullInt64
	var isPatch, isRead, isReplied, hasNotes any
	var subject, fromName, fromAddr, toAddr, ccAddr sql.NullString
	var date, inReplyTo, refsHdr, bodyPlain, flags, revCommit sql.NullString
	var epochTS sql.NullInt64

	err := row.Scan(
		&m.ID, &m.MessageID, &m.FolderID, &m.FolderName, &uid, &m.FilePath,
		&subject, &fromName, &fromAddr, &toAddr, &ccAddr,
		&date, &epochTS, &inReplyTo, &refsHdr,
		&bodyPlain, &isPatch, &isRead, &isReplied, &hasNotes,
		&flags, &revCommit, &m.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	if uid.Valid {
		u := uint32(uid.Int64)
		m.IMAPUID = &u
	}
	m.Subject = subject.String
	m.FromName = fromName.String
	m.FromAddr = fromAddr.String
	m.ToAddr = toAddr.String
	m.CcAddr = ccAddr.String
	m.Date = date.String
	m.EpochTimestamp = epochTS.Int64
	m.InReplyTo = inReplyTo.String
	m.ReferencesHeader = refsHdr.String
	m.BodyPlain = bodyPlain.String
	m.Flags = flags.String
	if revCommit.Valid {
		m.ReviewCommitID = revCommit.String
	}
	m.IsPatch = toBool(isPatch)
	m.IsRead = toBool(isRead)
	m.IsReplied = toBool(isReplied)
	m.HasNotes = toBool(hasNotes)

	if m.FolderName == "AI-reviews" || m.FromName == "AI review" {
		m.IsAIReview = true
		m.MessageID = ""
		m.IsPatch = false
		m.HasNotes = false
		m.ReviewCommitID = ""
		m.ReviewCommitIDHyphen = ""
	} else if m.ReviewCommitID != "" {
		m.ReviewCommitIDHyphen = m.ReviewCommitID
	}

	return &m, nil
}

const messageSelectColumns = `
	m.id, m.message_id, m.folder_id, f.name AS folder_name, m.imap_uid, m.file_path,
	m.subject, m.from_name, m.from_addr, m.to_addr, m.cc_addr,
	m.date, m.epoch_timestamp, m.in_reply_to, m.references_header,
	m.body_plain, m.is_patch, m.is_read, m.is_replied, m.has_notes,
	m.flags, m.review_commit_id, m.created_at
`

// GetMessageByID retrieves a message by its primary database ID.
func (d *Database) GetMessageByID(id int64) (*Message, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM messages m
		JOIN folders f ON m.folder_id = f.id
		WHERE m.id = ?;
	`, messageSelectColumns)
	return scanMessage(d.DB.QueryRow(query, id))
}

// GetMessageByMsgID retrieves a message by its Message-ID (stripping angle brackets).
func (d *Database) GetMessageByMsgID(msgID string) (*Message, error) {
	cleaned := parser.CleanMsgID(msgID)
	query := fmt.Sprintf(`
		SELECT %s
		FROM messages m
		JOIN folders f ON m.folder_id = f.id
		WHERE m.message_id = ? OR m.message_id = ?
		ORDER BY m.id DESC LIMIT 1;
	`, messageSelectColumns)
	return scanMessage(d.DB.QueryRow(query, cleaned, msgID))
}

// GetMessageByFolderAndMsgID retrieves a message within a specific folder.
func (d *Database) GetMessageByFolderAndMsgID(folderID int64, msgID string) (*Message, error) {
	cleaned := parser.CleanMsgID(msgID)
	query := fmt.Sprintf(`
		SELECT %s
		FROM messages m
		JOIN folders f ON m.folder_id = f.id
		WHERE m.folder_id = ? AND (m.message_id = ? OR m.message_id = ?);
	`, messageSelectColumns)
	return scanMessage(d.DB.QueryRow(query, folderID, cleaned, msgID))
}

// ListMessagesWithOptions lists messages filtered by folder, with limit, offset, sorting and optional threading.
func (d *Database) ListMessagesWithOptions(folderID *int64, limit, offset int, sortBy, sortOrder string, threaded bool) ([]*Message, error) {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("SELECT %s FROM messages m JOIN folders f ON m.folder_id = f.id ", messageSelectColumns))

	var args []any
	if folderID != nil {
		sb.WriteString("WHERE m.folder_id = ? ")
		args = append(args, *folderID)
	}

	if !threaded {
		sqlCol := "m.epoch_timestamp"
		switch strings.ToLower(sortBy) {
		case "subject":
			sqlCol = "m.subject"
		case "from":
			sqlCol = "m.from_name"
		}
		sqlOrder := "DESC"
		if strings.ToLower(sortOrder) == "asc" {
			sqlOrder = "ASC"
		}
		sb.WriteString(fmt.Sprintf("ORDER BY %s %s, m.id DESC ", sqlCol, sqlOrder))
	} else {
		sb.WriteString("ORDER BY m.epoch_timestamp DESC, m.id DESC ")
	}

	if limit > 0 {
		sb.WriteString("LIMIT ? ")
		args = append(args, limit)
	} else if offset > 0 {
		sb.WriteString("LIMIT -1 ")
	}
	if offset > 0 {
		sb.WriteString("OFFSET ? ")
		args = append(args, offset)
	}

	rows, err := d.DB.Query(sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list messages: %w", err)
	}
	defer rows.Close()

	var messages []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}

	if threaded {
		return ThreadMessages(messages, sortBy, sortOrder), nil
	}
	return messages, nil
}

// ListMessages lists messages optionally filtered by folder, with limit and offset.
func (d *Database) ListMessages(folderID *int64, limit, offset int) ([]*Message, error) {
	return d.ListMessagesWithOptions(folderID, limit, offset, "date", "desc", false)
}

// SetMessageRead marks a message as read or unread.
func (d *Database) SetMessageRead(id int64, read bool) error {
	val := 0
	if read {
		val = 1
	}
	_, err := d.DB.Exec("UPDATE messages SET is_read = ? WHERE id = ?;", val, id)
	return err
}

// SetMessageReplied marks a message as replied.
func (d *Database) SetMessageReplied(id int64, replied bool) error {
	val := 0
	if replied {
		val = 1
	}
	_, err := d.DB.Exec("UPDATE messages SET is_replied = ? WHERE id = ?;", val, id)
	return err
}

// SetMessageFlags updates flags string for a message.
func (d *Database) SetMessageFlags(id int64, flags string) error {
	_, err := d.DB.Exec("UPDATE messages SET flags = ? WHERE id = ?;", flags, id)
	return err
}

// SetMessageNotes updates has_notes flag for a message.
func (d *Database) SetMessageNotes(id int64, hasNotes bool) error {
	val := 0
	if hasNotes {
		val = 1
	}
	_, err := d.DB.Exec("UPDATE messages SET has_notes = ? WHERE id = ?;", val, id)
	return err
}

// SetMessageFilePath updates the file path (e.g. after Maildir rename or move).
func (d *Database) SetMessageFilePath(id int64, path string) error {
	_, err := d.DB.Exec("UPDATE messages SET file_path = ? WHERE id = ?;", path, id)
	return err
}

// UpdateMessageSubjectAndBody updates subject and body_plain for a message (used by review updates).
func (d *Database) UpdateMessageSubjectAndBody(id int64, subject, body string) error {
	_, err := d.DB.Exec("UPDATE messages SET subject = ?, body_plain = ? WHERE id = ?;", subject, body, id)
	return err
}

// SetMessageReviewCommit associates a git review commit ID with a message ID.
func (d *Database) SetMessageReviewCommit(msgID, commitID string) error {
	cleaned := parser.CleanMsgID(msgID)
	_, err := d.DB.Exec(`
		INSERT INTO review_commits (message_id, commit_id) VALUES (?, ?)
		ON CONFLICT(message_id) DO UPDATE SET commit_id = excluded.commit_id;
	`, cleaned, commitID)
	if err != nil {
		return err
	}
	_, err = d.DB.Exec("UPDATE messages SET review_commit_id = ? WHERE message_id = ?;", commitID, cleaned)
	return err
}

// GetReviewCommit retrieves the review commit ID for a message if one exists.
func (d *Database) GetReviewCommit(msgID string) (string, error) {
	cleaned := parser.CleanMsgID(msgID)
	var commitID string
	err := d.DB.QueryRow("SELECT commit_id FROM review_commits WHERE message_id = ?;", cleaned).Scan(&commitID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return commitID, err
}

// DeleteMessage deletes a message from the database.
func (d *Database) DeleteMessage(id int64) error {
	_, err := d.DB.Exec("DELETE FROM messages WHERE id = ?;", id)
	return err
}

// Search performs full-text search using SQLite FTS5 with matched snippets.
func (d *Database) Search(query string, limit int) ([]*SearchResult, error) {
	if limit <= 0 {
		limit = 20
	}

	cleanQuery := strings.TrimSpace(query)
	if cleanQuery == "" {
		return nil, nil
	}

	sqlQuery := `
	SELECT
		m.id,
		m.message_id,
		m.subject,
		m.from_addr,
		m.date,
		m.is_patch,
		snippet(messages_fts, 3, '<mark>', '</mark>', '...', 32) AS matched_snippet
	FROM messages_fts
	JOIN messages m ON messages_fts.rowid = m.id
	WHERE messages_fts MATCH ?
	ORDER BY rank
	LIMIT ?;
	`

	rows, err := d.DB.Query(sqlQuery, cleanQuery, limit)
	if err != nil {
		// If query had special characters causing FTS syntax error, try quoted phrase search
		escaped := fmt.Sprintf(`"%s"`, strings.ReplaceAll(cleanQuery, `"`, `""`))
		rows, err = d.DB.Query(sqlQuery, escaped, limit)
		if err != nil {
			return nil, fmt.Errorf("FTS5 search error: %w", err)
		}
	}
	defer rows.Close()

	var results []*SearchResult
	for rows.Next() {
		var r SearchResult
		var isPatch any
		if err := rows.Scan(&r.ID, &r.MessageID, &r.Subject, &r.FromAddr, &r.Date, &isPatch, &r.Snippet); err != nil {
			return nil, err
		}
		r.IsPatch = toBool(isPatch)
		results = append(results, &r)
	}
	return results, nil
}

// InsertHeaderSummary inserts or updates a message row using lightweight header bytes without a body or file.
func (d *Database) InsertHeaderSummary(
	folderID int64,
	imapUID uint32,
	headersBytes []byte,
	isRead bool,
	flags string,
) (int64, error) {
	parsed, err := parser.ParseEmail(headersBytes)
	if err != nil {
		return 0, fmt.Errorf("failed to parse email headers: %w", err)
	}

	msgID := parser.CleanMsgID(parsed.MessageID)
	if msgID == "" {
		msgID = fmt.Sprintf("gen-imap-%d-%d", folderID, imapUID)
	}

	isReplied := strings.Contains(flags, "R") ||
		strings.Contains(flags, `\Answered`) ||
		strings.Contains(flags, "Answered")

	// Check Review-Commit-ID
	revCommit, _ := d.GetReviewCommit(msgID)

	readVal := 0
	if isRead {
		readVal = 1
	}
	repliedVal := 0
	if isReplied {
		repliedVal = 1
	}

	query := `
	INSERT INTO messages (
		message_id, folder_id, imap_uid, file_path, subject, from_name, from_addr,
		to_addr, cc_addr, date, epoch_timestamp, in_reply_to, references_header,
		body_plain, is_patch, is_read, is_replied, has_notes, flags, review_commit_id
	) VALUES (?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?, '', 0, ?, ?, 0, ?, ?)
	ON CONFLICT(folder_id, message_id) DO UPDATE SET
		imap_uid = COALESCE(excluded.imap_uid, messages.imap_uid),
		subject = excluded.subject,
		from_name = excluded.from_name,
		from_addr = excluded.from_addr,
		to_addr = excluded.to_addr,
		cc_addr = excluded.cc_addr,
		date = excluded.date,
		epoch_timestamp = excluded.epoch_timestamp,
		in_reply_to = excluded.in_reply_to,
		references_header = excluded.references_header,
		is_read = excluded.is_read,
		is_replied = excluded.is_replied,
		flags = excluded.flags,
		review_commit_id = COALESCE(excluded.review_commit_id, messages.review_commit_id);
	`

	res, err := d.DB.Exec(
		query,
		msgID, folderID, imapUID, parsed.Subject, parsed.FromName, parsed.FromAddr,
		parsed.ToAddr, parsed.CcAddr, parsed.Date, parsed.EpochTimestamp, parsed.InReplyTo,
		parsed.ReferencesHeader, readVal, repliedVal, flags, revCommit,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert header summary: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil || id == 0 {
		var existingID int64
		_ = d.DB.QueryRow("SELECT id FROM messages WHERE folder_id = ? AND message_id = ?;", folderID, msgID).Scan(&existingID)
		return existingID, nil
	}
	return id, nil
}

// HydrateMessageBody updates a message row with its downloaded file path, plain body text, and patch status.
func (d *Database) HydrateMessageBody(id int64, filePath, bodyPlain string, isPatch bool) error {
	patchVal := 0
	if isPatch {
		patchVal = 1
	}
	_, err := d.DB.Exec(
		"UPDATE messages SET file_path = ?, body_plain = ?, is_patch = ? WHERE id = ?;",
		filePath, bodyPlain, patchVal, id,
	)
	return err
}

// GetFolderIMAPUIDs retrieves all known IMAP UIDs for a folder.
func (d *Database) GetFolderIMAPUIDs(folderID int64) ([]uint32, error) {
	rows, err := d.DB.Query("SELECT imap_uid FROM messages WHERE folder_id = ? AND imap_uid IS NOT NULL;", folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var uids []uint32
	for rows.Next() {
		var uid uint32
		if err := rows.Scan(&uid); err == nil {
			uids = append(uids, uid)
		}
	}
	return uids, nil
}

// DeleteMessagesByIMAPUIDs removes messages from the database matching the given IMAP UIDs, returning the deleted records.
func (d *Database) DeleteMessagesByIMAPUIDs(folderID int64, uids []uint32) ([]*Message, error) {
	if len(uids) == 0 {
		return nil, nil
	}

	var deleted []*Message
	for _, uid := range uids {
		var m Message
		var isPatch, isRead, isReplied, hasNotes any
		row := d.DB.QueryRow(`
			SELECT id, message_id, folder_id, imap_uid, file_path, subject, from_name, from_addr,
				   to_addr, cc_addr, date, epoch_timestamp, in_reply_to, references_header,
				   body_plain, is_patch, is_read, is_replied, has_notes, flags, review_commit_id, created_at
			FROM messages WHERE folder_id = ? AND imap_uid = ?;
		`, folderID, uid)
		err := row.Scan(
			&m.ID, &m.MessageID, &m.FolderID, &m.IMAPUID, &m.FilePath, &m.Subject, &m.FromName, &m.FromAddr,
			&m.ToAddr, &m.CcAddr, &m.Date, &m.EpochTimestamp, &m.InReplyTo, &m.ReferencesHeader,
			&m.BodyPlain, &isPatch, &isRead, &isReplied, &hasNotes, &m.Flags, &m.ReviewCommitID, &m.CreatedAt,
		)
		if err == nil {
			m.IsPatch = toBool(isPatch)
			m.IsRead = toBool(isRead)
			m.IsReplied = toBool(isReplied)
			m.HasNotes = toBool(hasNotes)
			deleted = append(deleted, &m)
			_, _ = d.DB.Exec("DELETE FROM messages WHERE id = ?;", m.ID)
		}
	}
	return deleted, nil
}

