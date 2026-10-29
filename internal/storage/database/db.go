package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"

	"undined-go/internal/constants"
)

// Database manages the SQLite connection pool and queries.
type Database struct {
	DB     *sql.DB
	DBPath string
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

	return &Database{
		DB:     db,
		DBPath: dbPath,
	}, nil
}

// GetDataVersion queries SQLite PRAGMA data_version to detect commits from external connections.
func (d *Database) GetDataVersion() (int64, error) {
	var version int64
	err := d.DB.QueryRow("PRAGMA data_version;").Scan(&version)
	return version, err
}

// Close closes the database connection.
func (d *Database) Close() error {
	return d.DB.Close()
}
