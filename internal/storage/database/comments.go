package database

import (
	"database/sql"
	"fmt"
	"strings"

	"undined-go/internal/storage/parser"
)

// PatchComment represents an inline review comment on a patch diff.
type PatchComment struct {
	ID          int64  `json:"id"`
	MessageID   string `json:"message_id"`
	MessageDBID *int64 `json:"message_db_id,omitempty"`
	FilePath    string `json:"file_path"`
	LineNo      int    `json:"line_no"`
	Side        string `json:"side"`
	Text        string `json:"text"`
	State       string `json:"state"`
	Author      string `json:"author"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// SavePatchComment inserts or updates an inline review comment.
func (d *Database) SavePatchComment(c *PatchComment) (*PatchComment, error) {
	cleanMsgID := parser.CleanMsgID(c.MessageID)
	if cleanMsgID == "" {
		return nil, fmt.Errorf("message_id cannot be empty")
	}
	c.MessageID = cleanMsgID

	side := strings.ToLower(strings.TrimSpace(c.Side))
	if side != "old" && side != "left" {
		side = "new"
	} else {
		side = "old"
	}
	c.Side = side

	state := strings.ToLower(strings.TrimSpace(c.State))
	if state == "" {
		state = "draft"
	}
	c.State = state

	author := strings.TrimSpace(c.Author)
	if author == "" {
		author = "user"
	}
	c.Author = author

	// If MessageDBID not set, look it up by MessageID
	if c.MessageDBID == nil {
		var dbID int64
		err := d.DB.QueryRow("SELECT id FROM messages WHERE message_id = ? LIMIT 1;", cleanMsgID).Scan(&dbID)
		if err == nil {
			c.MessageDBID = &dbID
		}
	}

	if c.ID > 0 {
		query := `
		UPDATE patch_comments
		SET file_path = ?, line_no = ?, side = ?, text = ?, state = ?, author = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?;
		`
		_, err := d.DB.Exec(query, c.FilePath, c.LineNo, c.Side, c.Text, c.State, c.Author, c.ID)
		if err != nil {
			return nil, err
		}
		d.MarkInProcessWrite()
		return c, nil
	}

	query := `
	INSERT INTO patch_comments (
		message_id, message_db_id, file_path, line_no, side, text, state, author
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?);
	`
	res, err := d.DB.Exec(
		query,
		c.MessageID,
		c.MessageDBID,
		c.FilePath,
		c.LineNo,
		c.Side,
		c.Text,
		c.State,
		c.Author,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to save comment: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	c.ID = id

	// Mark message as having notes
	_, _ = d.DB.Exec("UPDATE messages SET has_notes = 1 WHERE message_id = ?;", cleanMsgID)
	d.MarkInProcessWrite()

	return c, nil
}

// GetMessageComments retrieves review comments for a message, optionally filtered by state (e.g. 'draft' or 'submitted').
func (d *Database) GetMessageComments(msgID string, state ...string) ([]*PatchComment, error) {
	cleanMsgID := parser.CleanMsgID(msgID)
	query := `
	SELECT id, message_id, message_db_id, file_path, line_no, side, text, state, author, created_at, updated_at
	FROM patch_comments
	WHERE message_id = ?
`
	args := []any{cleanMsgID}
	if len(state) > 0 && strings.TrimSpace(state[0]) != "" {
		query += " AND state = ?"
		args = append(args, strings.TrimSpace(state[0]))
	}
	query += " ORDER BY file_path ASC, line_no ASC, id ASC;"

	rows, err := d.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	comments := make([]*PatchComment, 0)
	for rows.Next() {
		var c PatchComment
		var dbID sql.NullInt64
		if err := rows.Scan(
			&c.ID, &c.MessageID, &dbID, &c.FilePath, &c.LineNo,
			&c.Side, &c.Text, &c.State, &c.Author, &c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if dbID.Valid {
			c.MessageDBID = &dbID.Int64
		}
		comments = append(comments, &c)
	}
	return comments, nil
}

// DiscardDraftComments removes all comments in 'draft' state (or legacy 'submitted') for the message and returns the discarded count.
func (d *Database) DiscardDraftComments(msgID string) (int64, error) {
	cleanMsgID := parser.CleanMsgID(msgID)
	res, err := d.DB.Exec("DELETE FROM patch_comments WHERE message_id = ? AND (state = 'draft' OR state = 'submitted' OR state = '' OR state IS NULL);", cleanMsgID)
	if err != nil {
		return 0, err
	}
	count, _ := res.RowsAffected()

	// Update has_notes flag if no comments remain
	var remaining int
	_ = d.DB.QueryRow("SELECT COUNT(*) FROM patch_comments WHERE message_id = ?;", cleanMsgID).Scan(&remaining)
	hasNotes := 0
	if remaining > 0 {
		hasNotes = 1
	}
	_, _ = d.DB.Exec("UPDATE messages SET has_notes = ? WHERE message_id = ?;", hasNotes, cleanMsgID)
	d.MarkInProcessWrite()
	return count, nil
}

// DeleteComment deletes a single comment by ID.
func (d *Database) DeleteComment(commentID int64) error {
	var msgID string
	_ = d.DB.QueryRow("SELECT message_id FROM patch_comments WHERE id = ?;", commentID).Scan(&msgID)

	_, err := d.DB.Exec("DELETE FROM patch_comments WHERE id = ?;", commentID)
	if err != nil {
		return err
	}

	if msgID != "" {
		var count int
		_ = d.DB.QueryRow("SELECT COUNT(*) FROM patch_comments WHERE message_id = ?;", msgID).Scan(&count)
		hasNotes := 0
		if count > 0 {
			hasNotes = 1
		}
		_, _ = d.DB.Exec("UPDATE messages SET has_notes = ? WHERE message_id = ?;", hasNotes, msgID)
	}
	d.MarkInProcessWrite()
	return nil
}
