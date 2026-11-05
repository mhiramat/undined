package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "github.com/mattn/go-sqlite3"

	"undined-go/internal/constants"
)

// Database manages the SQLite connection pool and queries.
type Database struct {
	DB                     *sql.DB
	watcherDB              *sql.DB
	DBPath                 string
	watcherMu              sync.Mutex
	baselineVersion        int64
	mainBaselineVersion    int64
	externalChangeDetected bool
}

// NewDatabase opens or creates the SQLite database at dbPath, running migrations.
func NewDatabase(dbPath string) (*Database, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	dsn := fmt.Sprintf("%s?_timeout=%d&_journal=WAL&_fk=1",
		dbPath, int(constants.DefaultSQLiteTimeoutSeconds*1000))

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database %s: %w", dbPath, err)
	}

	// SQLite WAL mode supports concurrent readers and 1 writer
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)

	if err := initDB(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	watcherDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to open sqlite watcher connection %s: %w", dbPath, err)
	}
	watcherDB.SetMaxOpenConns(1)
	watcherDB.SetMaxIdleConns(1)

	return &Database{
		DB:        db,
		watcherDB: watcherDB,
		DBPath:    dbPath,
	}, nil
}

func (d *Database) getWatcherVersionLocked() (int64, error) {
	conn := d.watcherDB
	if conn == nil {
		conn = d.DB
	}
	var version int64
	err := conn.QueryRow("PRAGMA data_version;").Scan(&version)
	return version, err
}

func (d *Database) getMainVersionLocked() (int64, error) {
	var version int64
	err := d.DB.QueryRow("PRAGMA data_version;").Scan(&version)
	return version, err
}

// GetDataVersion queries SQLite PRAGMA data_version to detect commits from external connections.
func (d *Database) GetDataVersion() (int64, error) {
	d.watcherMu.Lock()
	defer d.watcherMu.Unlock()
	return d.getWatcherVersionLocked()
}

// MarkInProcessWrite records that a mutation was made by this in-process Database instance.
func (d *Database) MarkInProcessWrite() {
	d.watcherMu.Lock()
	defer d.watcherMu.Unlock()
	if mainVer, err := d.getMainVersionLocked(); err == nil {
		if d.mainBaselineVersion > 0 && mainVer != d.mainBaselineVersion {
			d.externalChangeDetected = true
		}
		d.mainBaselineVersion = mainVer
	}
	if watchVer, err := d.getWatcherVersionLocked(); err == nil {
		d.baselineVersion = watchVer
	}
}

// SetBaselineVersion sets the initial baseline data_version and resets in-process write tracking.
func (d *Database) SetBaselineVersion(ver int64) {
	d.watcherMu.Lock()
	defer d.watcherMu.Unlock()
	d.baselineVersion = ver
	if mainVer, err := d.getMainVersionLocked(); err == nil {
		d.mainBaselineVersion = mainVer
	}
	d.externalChangeDetected = false
}

// CheckAndResetExternalChange checks if currVer indicates a change made by an external process.
// If an in-process write accounted for the version change, returns false.
func (d *Database) CheckAndResetExternalChange(currVer int64) bool {
	d.watcherMu.Lock()
	defer d.watcherMu.Unlock()

	if mainVer, err := d.getMainVersionLocked(); err == nil {
		d.mainBaselineVersion = mainVer
	}

	if d.externalChangeDetected {
		d.externalChangeDetected = false
		d.baselineVersion = currVer
		return true
	}

	if d.baselineVersion == 0 {
		d.baselineVersion = currVer
		return false
	}

	if currVer == d.baselineVersion {
		return false
	}

	d.baselineVersion = currVer
	return true
}

// Close closes the database connection.
func (d *Database) Close() error {
	if d.watcherDB != nil {
		// Best-effort close of auxiliary read-only watcher connection before closing primary DB.
		_ = d.watcherDB.Close()
	}
	return d.DB.Close()
}
