package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func setupTestDBForAddresses(t *testing.T) (*Database, func()) {
	tmpDir, err := os.MkdirTemp("", "undined-addr-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}

	if err := initDB(db); err != nil {
		t.Fatalf("initDB failed: %v", err)
	}

	database := &Database{DB: db}
	cleanup := func() {
		_ = db.Close()
		_ = os.RemoveAll(tmpDir)
	}
	return database, cleanup
}

func TestRecordKnownAddress(t *testing.T) {
	d, cleanup := setupTestDBForAddresses(t)
	defer cleanup()

	// Initial insert
	err := d.RecordKnownAddress("Kernel Developer", "dev@example.com")
	if err != nil {
		t.Fatalf("RecordKnownAddress failed: %v", err)
	}

	addrs, err := d.SearchKnownAddresses("dev@example.com", 10)
	if err != nil {
		t.Fatalf("SearchKnownAddresses failed: %v", err)
	}
	if len(addrs) != 1 {
		t.Fatalf("expected 1 address, got %d", len(addrs))
	}
	if addrs[0].Frequency != 1 || addrs[0].Name != "Kernel Developer" {
		t.Errorf("unexpected record: %+v", addrs[0])
	}

	// Second occurrence increments frequency
	err = d.RecordKnownAddress("Kernel Developer", "dev@example.com")
	if err != nil {
		t.Fatalf("second RecordKnownAddress failed: %v", err)
	}

	addrs, _ = d.SearchKnownAddresses("dev@example.com", 10)
	if len(addrs) != 1 || addrs[0].Frequency != 2 {
		t.Errorf("expected frequency 2, got %+v", addrs)
	}

	// Update empty name with actual name
	err = d.RecordKnownAddress("", "rostedt@example.org")
	if err != nil {
		t.Fatalf("inserting empty name failed: %v", err)
	}
	err = d.RecordKnownAddress("Steven Rostedt", "rostedt@example.org")
	if err != nil {
		t.Fatalf("updating name failed: %v", err)
	}

	addrs, _ = d.SearchKnownAddresses("rostedt@example.org", 10)
	if len(addrs) != 1 || addrs[0].Name != "Steven Rostedt" {
		t.Errorf("expected name to be updated to 'Steven Rostedt', got %q", addrs[0].Name)
	}
}

func TestCollectKnownAddressesFromMessages(t *testing.T) {
	d, cleanup := setupTestDBForAddresses(t)
	defer cleanup()

	// Insert messages with To and Cc headers
	folder, err := d.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("failed to create folder: %v", err)
	}

	_, err = d.DB.Exec(`
		INSERT INTO messages (
			message_id, folder_id, file_path, subject, from_addr, to_addr, cc_addr
		) VALUES
		('msg-1@kernel.org', ?, 'path1', 'Subj 1', 'dev@example.com',
		 '"Kernel Developer" <dev@example.com>, linux-trace-kernel@vger.kernel.org',
		 'Reviewer <reviewer@kernel.org>'),
		('msg-2@kernel.org', ?, 'path2', 'Subj 2', 'author@kernel.org',
		 'dev@example.com',
		 'Reviewer <reviewer@kernel.org>, linux-kernel@vger.kernel.org');
	`, folder.ID, folder.ID)
	if err != nil {
		t.Fatalf("failed to insert test messages: %v", err)
	}

	count, err := d.CollectKnownAddresses()
	if err != nil {
		t.Fatalf("CollectKnownAddresses failed: %v", err)
	}
	if count < 4 {
		t.Errorf("expected at least 4 address entries processed, got %d", count)
	}

	total, err := d.GetKnownAddressCount()
	if err != nil {
		t.Fatalf("GetKnownAddressCount failed: %v", err)
	}
	if total != 4 {
		t.Errorf("expected 4 unique addresses, got %d", total)
	}

	// Search dev@example.com: frequency should be 2
	devList, _ := d.SearchKnownAddresses("dev@example.com", 10)
	if len(devList) != 1 || devList[0].Frequency != 2 {
		t.Errorf("expected dev@example.com to have frequency 2, got %+v", devList)
	}
}

func TestSearchKnownAddressesRanking(t *testing.T) {
	d, cleanup := setupTestDBForAddresses(t)
	defer cleanup()

	_ = d.RecordKnownAddress("Alice Developer", "alice@example.com")
	_ = d.RecordKnownAddress("Bob Maintainer", "bob@example.com")
	_ = d.RecordKnownAddress("Bob Maintainer", "bob@example.com") // freq 2
	_ = d.RecordKnownAddress("Charlie Reviewer", "charlie@example.com")

	// Search "bob"
	results, err := d.SearchKnownAddresses("bob", 10)
	if err != nil {
		t.Fatalf("SearchKnownAddresses failed: %v", err)
	}
	if len(results) != 1 || results[0].Address != "bob@example.com" {
		t.Errorf("expected bob@example.com, got %+v", results)
	}

	// Empty query returns sorted by frequency DESC
	all, err := d.SearchKnownAddresses("", 10)
	if err != nil {
		t.Fatalf("empty search failed: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 results, got %d", len(all))
	}
	if all[0].Address != "bob@example.com" {
		t.Errorf("expected highest frequency (bob@example.com) first, got %s", all[0].Address)
	}

	// Delete known address
	err = d.DeleteKnownAddress(all[0].ID)
	if err != nil {
		t.Fatalf("DeleteKnownAddress failed: %v", err)
	}
	remaining, _ := d.GetKnownAddressCount()
	if remaining != 2 {
		t.Errorf("expected 2 remaining addresses after delete, got %d", remaining)
	}
}
