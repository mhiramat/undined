package database

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPatchSeriesDraftCRUD(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "series.db")

	db, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase failed: %v", err)
	}
	defer db.Close()

	series := &PatchSeriesDraft{
		SeriesID:   "ser_test001",
		SeriesKey:  "topic/fprobe-cleanup:v2",
		Branch:     "topic/fprobe-cleanup",
		Version:    2,
		BaseCommit: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
		ToAddrs:    []string{"linux-kernel@vger.kernel.org"},
		CcAddrs:    nil, // intentionally nil to verify non-null JSON slice behavior
		InReplyTo:  "<prev-v1@kernel.org>",
		Status:     SeriesStatusDraft,
	}
	items := []*PatchSeriesItem{
		{
			SeriesID:      series.SeriesID,
			SeqIndex:      0,
			IsCoverLetter: true,
			DraftMsgID:    "101",
			RFCMessageID:  "<20261123112500.12345-1-dev@kernel.org>",
			Subject:       "[PATCH v2 0/2] fprobe: cleanup series",
			Status:        SeriesItemStatusDraft,
		},
		{
			SeriesID:     series.SeriesID,
			SeqIndex:     1,
			DraftMsgID:   "102",
			RFCMessageID: "<20261123112500.12345-2-dev@kernel.org>",
			Subject:      "[PATCH v2 1/2] fprobe: simplify entry",
			CommitSHA:    "1111111111111111111111111111111111111111",
			ExtraCc:      []string{"reviewer@example.com"},
			Status:       SeriesItemStatusDraft,
		},
		{
			SeriesID:     series.SeriesID,
			SeqIndex:     2,
			DraftMsgID:   "103",
			RFCMessageID: "<20261123112500.12345-3-dev@kernel.org>",
			Subject:      "[PATCH v2 2/2] fprobe: simplify exit",
			CommitSHA:    "2222222222222222222222222222222222222222",
			Status:       SeriesItemStatusDraft,
		},
	}

	if err := db.CreatePatchSeriesDraft(series, items); err != nil {
		t.Fatalf("CreatePatchSeriesDraft failed: %v", err)
	}

	got, err := db.GetPatchSeriesDraft(series.SeriesID)
	if err != nil {
		t.Fatalf("GetPatchSeriesDraft failed: %v", err)
	}
	if got == nil {
		t.Fatalf("expected series, got nil")
	}
	if got.SeriesKey != series.SeriesKey || got.Version != 2 || got.SendDelaySec != DefaultSeriesSendDelaySec {
		t.Errorf("unexpected series metadata: %+v", got)
	}
	if len(got.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(got.Items))
	}
	if !got.Items[0].IsCoverLetter || got.Items[0].SeqIndex != 0 {
		t.Errorf("expected item 0 to be cover letter, got %+v", got.Items[0])
	}
	if len(got.Items[1].ExtraCc) != 1 || got.Items[1].ExtraCc[0] != "reviewer@example.com" {
		t.Errorf("unexpected extra_cc on item 1: %+v", got.Items[1].ExtraCc)
	}

	// Verify no null JSON arrays when marshaling
	rawJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if strings.Contains(string(rawJSON), ":null") {
		t.Errorf("expected non-null arrays in JSON, got %s", string(rawJSON))
	}

	// Lookup by key
	byKey, err := db.GetPatchSeriesDraftByKey(series.SeriesKey)
	if err != nil || byKey == nil || byKey.SeriesID != series.SeriesID {
		t.Fatalf("GetPatchSeriesDraftByKey failed: got=%+v err=%v", byKey, err)
	}

	// List all
	all, err := db.ListPatchSeriesDrafts()
	if err != nil || len(all) != 1 {
		t.Fatalf("ListPatchSeriesDrafts expected 1, got %d (err=%v)", len(all), err)
	}

	// Update headers
	if err := db.UpdatePatchSeriesHeaders(series.SeriesID, []string{"maintainer@kernel.org"}, []string{"list@vger.kernel.org"}, "", 5); err != nil {
		t.Fatalf("UpdatePatchSeriesHeaders failed: %v", err)
	}
	got, err = db.GetPatchSeriesDraft(series.SeriesID)
	if err != nil || got == nil {
		t.Fatalf("GetPatchSeriesDraft after header update failed: got=%+v, err=%v", got, err)
	}
	if got.SendDelaySec != 5 || len(got.ToAddrs) != 1 || got.ToAddrs[0] != "maintainer@kernel.org" {
		t.Errorf("headers not updated: got %+v", got)
	}

	// HasAnySentItem before and after marking item 0 sent
	hasSent, err := db.HasAnySentItem(series.SeriesID)
	if err != nil || hasSent {
		t.Fatalf("HasAnySentItem() = (%v, %v), want (false, nil)", hasSent, err)
	}

	now := time.Now().UTC()
	if err := db.UpdatePatchSeriesItemStatus(series.SeriesID, 0, SeriesItemStatusSent, "", &now); err != nil {
		t.Fatalf("UpdatePatchSeriesItemStatus failed: %v", err)
	}
	hasSent, err = db.HasAnySentItem(series.SeriesID)
	if err != nil || !hasSent {
		t.Fatalf("HasAnySentItem() = (%v, %v), want (true, nil)", hasSent, err)
	}

	if err := db.UpdatePatchSeriesStatus(series.SeriesID, SeriesStatusPaused); err != nil {
		t.Fatalf("UpdatePatchSeriesStatus failed: %v", err)
	}
	got, err = db.GetPatchSeriesDraft(series.SeriesID)
	if err != nil || got == nil {
		t.Fatalf("GetPatchSeriesDraft after status update failed: got=%+v, err=%v", got, err)
	}
	if got.Status != SeriesStatusPaused || got.Items[0].Status != SeriesItemStatusSent || got.Items[0].SentAt == nil {
		t.Errorf("unexpected status after update: series=%+v item0=%+v", got, got.Items[0])
	}

	// Delete cascades to patch_series_items
	if err := db.DeletePatchSeriesDraft(series.SeriesID); err != nil {
		t.Fatalf("DeletePatchSeriesDraft failed: %v", err)
	}
	deleted, err := db.GetPatchSeriesDraft(series.SeriesID)
	if err != nil || deleted != nil {
		t.Fatalf("expected nil after delete, got %+v (err=%v)", deleted, err)
	}
	var remainingItems int
	if err := db.DB.QueryRow("SELECT COUNT(*) FROM patch_series_items WHERE series_id = ?;", series.SeriesID).Scan(&remainingItems); err != nil {
		t.Fatalf("count remaining items failed: %v", err)
	}
	if remainingItems != 0 {
		t.Errorf("expected ON DELETE CASCADE to remove items, found %d", remainingItems)
	}
}
