package database

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Patch series draft and item status constants.
const (
	SeriesStatusDraft   = "draft"
	SeriesStatusSending = "sending"
	SeriesStatusPaused  = "paused"
	SeriesStatusFailed  = "failed"
	SeriesStatusSent    = "sent"

	SeriesItemStatusDraft   = "draft"
	SeriesItemStatusSending = "sending"
	SeriesItemStatusSent    = "sent"
	SeriesItemStatusFailed  = "failed"

	DefaultSeriesSendDelaySec = 2
)

// PatchSeriesDraft represents a grouped patch series draft queue in SQLite.
type PatchSeriesDraft struct {
	SeriesID     string             `json:"series_id"`
	SeriesKey    string             `json:"series_key"`
	Branch       string             `json:"branch"`
	Version      int                `json:"version"`
	BaseCommit   string             `json:"base_commit"`
	ToAddrs      []string           `json:"to_addrs"`
	CcAddrs      []string           `json:"cc_addrs"`
	InReplyTo    string             `json:"in_reply_to"`
	Status       string             `json:"status"`
	SendDelaySec int                `json:"send_delay_sec"`
	CreatedAt    string             `json:"created_at"`
	UpdatedAt    string             `json:"updated_at"`
	Items        []*PatchSeriesItem `json:"items"`
}

// PatchSeriesItem represents a single cover letter or patch email in a series draft queue.
type PatchSeriesItem struct {
	SeriesID      string   `json:"series_id"`
	SeqIndex      int      `json:"seq_index"`
	IsCoverLetter bool     `json:"is_cover_letter"`
	DraftMsgID    string   `json:"draft_msg_id"`
	RFCMessageID  string   `json:"rfc_message_id"`
	Subject       string   `json:"subject"`
	CommitSHA     string   `json:"commit_sha"`
	ExtraTo       []string `json:"extra_to"`
	ExtraCc       []string `json:"extra_cc"`
	Status        string   `json:"status"`
	ErrorMessage  string   `json:"error_message"`
	SentAt        *string  `json:"sent_at,omitempty"`
}

func marshalStringSliceJSON(s []string) (string, error) {
	if s == nil {
		s = make([]string, 0)
	}
	b, err := json.Marshal(s)
	if err != nil {
		return "[]", err
	}
	return string(b), nil
}

func unmarshalStringSliceJSON(raw string) []string {
	if raw == "" {
		return make([]string, 0)
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return make([]string, 0)
	}
	return out
}

// CreatePatchSeriesDraft inserts a new patch series draft queue and its ordered items atomically.
func (d *Database) CreatePatchSeriesDraft(series *PatchSeriesDraft, items []*PatchSeriesItem) error {
	if series == nil {
		return errors.New("series cannot be nil")
	}
	if series.SeriesID == "" {
		return errors.New("series_id is required")
	}
	if series.SeriesKey == "" {
		return errors.New("series_key is required")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if series.CreatedAt == "" {
		series.CreatedAt = now
	}
	if series.UpdatedAt == "" {
		series.UpdatedAt = now
	}
	if series.Status == "" {
		series.Status = SeriesStatusDraft
	}
	if series.Version <= 0 {
		series.Version = 1
	}
	if series.SendDelaySec <= 0 {
		series.SendDelaySec = DefaultSeriesSendDelaySec
	}

	toJSON, err := marshalStringSliceJSON(series.ToAddrs)
	if err != nil {
		return fmt.Errorf("marshal to_addrs: %w", err)
	}
	ccJSON, err := marshalStringSliceJSON(series.CcAddrs)
	if err != nil {
		return fmt.Errorf("marshal cc_addrs: %w", err)
	}

	tx, err := d.DB.Begin()
	if err != nil {
		return fmt.Errorf("begin series transaction: %w", err)
	}
	defer func() {
		// Safe to ignore: returns sql.ErrTxDone once Commit succeeds.
		_ = tx.Rollback()
	}()

	_, err = tx.Exec(`
		INSERT INTO patch_series_drafts (
			series_id, series_key, branch, version, base_commit,
			to_addrs, cc_addrs, in_reply_to, status, send_delay_sec,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`,
		series.SeriesID, series.SeriesKey, series.Branch, series.Version, series.BaseCommit,
		toJSON, ccJSON, series.InReplyTo, series.Status, series.SendDelaySec,
		series.CreatedAt, series.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert patch_series_drafts: %w", err)
	}

	for _, item := range items {
		if item == nil {
			continue
		}
		itemStatus := item.Status
		if itemStatus == "" {
			itemStatus = SeriesItemStatusDraft
		}
		extraToJSON, err := marshalStringSliceJSON(item.ExtraTo)
		if err != nil {
			return fmt.Errorf("marshal item %d extra_to: %w", item.SeqIndex, err)
		}
		extraCcJSON, err := marshalStringSliceJSON(item.ExtraCc)
		if err != nil {
			return fmt.Errorf("marshal item %d extra_cc: %w", item.SeqIndex, err)
		}
		isCover := 0
		if item.IsCoverLetter {
			isCover = 1
		}

		_, err = tx.Exec(`
			INSERT INTO patch_series_items (
				series_id, seq_index, is_cover_letter, draft_msg_id, rfc_message_id,
				subject, commit_sha, extra_to, extra_cc, status, error_message, sent_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
		`,
			series.SeriesID, item.SeqIndex, isCover, item.DraftMsgID, item.RFCMessageID,
			item.Subject, item.CommitSHA, extraToJSON, extraCcJSON, itemStatus, item.ErrorMessage, item.SentAt,
		)
		if err != nil {
			return fmt.Errorf("insert patch_series_items seq %d: %w", item.SeqIndex, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit series transaction: %w", err)
	}
	d.MarkInProcessWrite()
	return nil
}

func scanPatchSeriesDraft(scanner interface {
	Scan(dest ...any) error
}) (*PatchSeriesDraft, error) {
	var s PatchSeriesDraft
	var toJSON, ccJSON string
	err := scanner.Scan(
		&s.SeriesID, &s.SeriesKey, &s.Branch, &s.Version, &s.BaseCommit,
		&toJSON, &ccJSON, &s.InReplyTo, &s.Status, &s.SendDelaySec,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	s.ToAddrs = unmarshalStringSliceJSON(toJSON)
	s.CcAddrs = unmarshalStringSliceJSON(ccJSON)
	s.Items = make([]*PatchSeriesItem, 0)
	return &s, nil
}

func (d *Database) listPatchSeriesItems(seriesID string) ([]*PatchSeriesItem, error) {
	rows, err := d.DB.Query(`
		SELECT series_id, seq_index, is_cover_letter, draft_msg_id, rfc_message_id,
		       subject, commit_sha, extra_to, extra_cc, status, error_message, sent_at
		FROM patch_series_items
		WHERE series_id = ?
		ORDER BY seq_index ASC;
	`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("query patch_series_items: %w", err)
	}
	defer rows.Close()

	items := make([]*PatchSeriesItem, 0)
	for rows.Next() {
		var item PatchSeriesItem
		var isCover int
		var extraToJSON, extraCcJSON string
		var sentAt sql.NullString
		if err := rows.Scan(
			&item.SeriesID, &item.SeqIndex, &isCover, &item.DraftMsgID, &item.RFCMessageID,
			&item.Subject, &item.CommitSHA, &extraToJSON, &extraCcJSON, &item.Status, &item.ErrorMessage, &sentAt,
		); err != nil {
			return nil, fmt.Errorf("scan patch_series_item: %w", err)
		}
		item.IsCoverLetter = isCover != 0
		item.ExtraTo = unmarshalStringSliceJSON(extraToJSON)
		item.ExtraCc = unmarshalStringSliceJSON(extraCcJSON)
		if sentAt.Valid && sentAt.String != "" {
			val := sentAt.String
			item.SentAt = &val
		}
		items = append(items, &item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate patch_series_items: %w", err)
	}
	return items, nil
}

// GetPatchSeriesDraft retrieves a patch series draft and its ordered items by series_id.
// Returns (nil, nil) if not found.
func (d *Database) GetPatchSeriesDraft(seriesID string) (*PatchSeriesDraft, error) {
	row := d.DB.QueryRow(`
		SELECT series_id, series_key, branch, version, base_commit,
		       to_addrs, cc_addrs, in_reply_to, status, send_delay_sec,
		       created_at, updated_at
		FROM patch_series_drafts
		WHERE series_id = ?;
	`, seriesID)

	series, err := scanPatchSeriesDraft(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get patch_series_draft %s: %w", seriesID, err)
	}

	items, err := d.listPatchSeriesItems(series.SeriesID)
	if err != nil {
		return nil, err
	}
	series.Items = items
	return series, nil
}

// GetPatchSeriesDraftByKey retrieves the most recently updated patch series draft matching series_key.
// Returns (nil, nil) if not found.
func (d *Database) GetPatchSeriesDraftByKey(seriesKey string) (*PatchSeriesDraft, error) {
	row := d.DB.QueryRow(`
		SELECT series_id, series_key, branch, version, base_commit,
		       to_addrs, cc_addrs, in_reply_to, status, send_delay_sec,
		       created_at, updated_at
		FROM patch_series_drafts
		WHERE series_key = ?
		ORDER BY updated_at DESC
		LIMIT 1;
	`, seriesKey)

	series, err := scanPatchSeriesDraft(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get patch_series_draft by key %s: %w", seriesKey, err)
	}

	items, err := d.listPatchSeriesItems(series.SeriesID)
	if err != nil {
		return nil, err
	}
	series.Items = items
	return series, nil
}

// ListPatchSeriesDrafts returns all patch series drafts ordered by updated_at descending.
func (d *Database) ListPatchSeriesDrafts() ([]*PatchSeriesDraft, error) {
	rows, err := d.DB.Query(`
		SELECT series_id, series_key, branch, version, base_commit,
		       to_addrs, cc_addrs, in_reply_to, status, send_delay_sec,
		       created_at, updated_at
		FROM patch_series_drafts
		ORDER BY updated_at DESC;
	`)
	if err != nil {
		return nil, fmt.Errorf("list patch_series_drafts: %w", err)
	}
	defer rows.Close()

	list := make([]*PatchSeriesDraft, 0)
	for rows.Next() {
		series, err := scanPatchSeriesDraft(rows)
		if err != nil {
			return nil, fmt.Errorf("scan patch_series_draft: %w", err)
		}
		list = append(list, series)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate patch_series_drafts: %w", err)
	}

	for _, series := range list {
		items, err := d.listPatchSeriesItems(series.SeriesID)
		if err != nil {
			return nil, err
		}
		series.Items = items
	}
	return list, nil
}

// HasAnySentItem reports whether any item in the given series has status 'sent'.
func (d *Database) HasAnySentItem(seriesID string) (bool, error) {
	var count int
	err := d.DB.QueryRow(`
		SELECT COUNT(*) FROM patch_series_items
		WHERE series_id = ? AND status = ?;
	`, seriesID, SeriesItemStatusSent).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check sent items for series %s: %w", seriesID, err)
	}
	return count > 0, nil
}

// DeletePatchSeriesDraft removes a patch series draft and cascades deletion to its items.
func (d *Database) DeletePatchSeriesDraft(seriesID string) error {
	_, err := d.DB.Exec("DELETE FROM patch_series_drafts WHERE series_id = ?;", seriesID)
	if err != nil {
		return fmt.Errorf("delete patch_series_draft %s: %w", seriesID, err)
	}
	d.MarkInProcessWrite()
	return nil
}

// UpdatePatchSeriesStatus updates the status and updated_at timestamp of a series draft.
func (d *Database) UpdatePatchSeriesStatus(seriesID, status string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.DB.Exec(`
		UPDATE patch_series_drafts
		SET status = ?, updated_at = ?
		WHERE series_id = ?;
	`, status, now, seriesID)
	if err != nil {
		return fmt.Errorf("update series status %s: %w", seriesID, err)
	}
	d.MarkInProcessWrite()
	return nil
}

// UpdatePatchSeriesItemStatus updates a single series item's status, error message, and optional sent_at.
func (d *Database) UpdatePatchSeriesItemStatus(seriesID string, seqIndex int, status, errMsg string, sentAt *time.Time) error {
	var sentAtVal any
	if sentAt != nil {
		sentAtVal = sentAt.UTC().Format(time.RFC3339)
	}
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := d.DB.Begin()
	if err != nil {
		return fmt.Errorf("begin item status update: %w", err)
	}
	defer func() {
		// Safe to ignore: returns sql.ErrTxDone once Commit succeeds.
		_ = tx.Rollback()
	}()

	if _, err := tx.Exec(`
		UPDATE patch_series_items
		SET status = ?, error_message = ?, sent_at = COALESCE(?, sent_at)
		WHERE series_id = ? AND seq_index = ?;
	`, status, errMsg, sentAtVal, seriesID, seqIndex); err != nil {
		return fmt.Errorf("update item status %s[%d]: %w", seriesID, seqIndex, err)
	}

	if _, err := tx.Exec(`
		UPDATE patch_series_drafts
		SET updated_at = ?
		WHERE series_id = ?;
	`, now, seriesID); err != nil {
		return fmt.Errorf("update series timestamp %s: %w", seriesID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit item status update: %w", err)
	}
	d.MarkInProcessWrite()
	return nil
}

// UpdatePatchSeriesHeaders updates shared To, Cc, In-Reply-To, and send_delay_sec for a series draft.
func (d *Database) UpdatePatchSeriesHeaders(seriesID string, toAddrs, ccAddrs []string, inReplyTo string, sendDelaySec int) error {
	toJSON, err := marshalStringSliceJSON(toAddrs)
	if err != nil {
		return fmt.Errorf("marshal to_addrs: %w", err)
	}
	ccJSON, err := marshalStringSliceJSON(ccAddrs)
	if err != nil {
		return fmt.Errorf("marshal cc_addrs: %w", err)
	}
	if sendDelaySec <= 0 {
		sendDelaySec = DefaultSeriesSendDelaySec
	}
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = d.DB.Exec(`
		UPDATE patch_series_drafts
		SET to_addrs = ?, cc_addrs = ?, in_reply_to = ?, send_delay_sec = ?, updated_at = ?
		WHERE series_id = ?;
	`, toJSON, ccJSON, inReplyTo, sendDelaySec, now, seriesID)
	if err != nil {
		return fmt.Errorf("update series headers %s: %w", seriesID, err)
	}
	d.MarkInProcessWrite()
	return nil
}
