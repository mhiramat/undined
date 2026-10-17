package database

import (
	"database/sql"
	"fmt"
	"strings"

	"undined-go/internal/constants"
)

// Folder represents a Maildir/IMAP folder record in SQLite.
type Folder struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	LastSyncedUID uint32 `json:"last_synced_uid"`
	SyncEnabled   bool   `json:"sync_enabled"`
	IsIMAP        bool   `json:"is_imap"`
	UnreadCount   int    `json:"unread_count"`
	TotalCount    int    `json:"total_count"`
}

// IsStandardFolder checks if the folder is one of the built-in system folders.
func IsStandardFolder(name string) bool {
	for _, sf := range constants.StandardFolders {
		if strings.EqualFold(sf, name) {
			return true
		}
	}
	return false
}

func toBool(v any) bool {
	switch val := v.(type) {
	case bool:
		return val
	case int64:
		return val == 1
	case int:
		return val == 1
	case string:
		return val == "1" || strings.EqualFold(val, "true")
	default:
		return false
	}
}

// GetOrCreateFolder retrieves a folder by name or creates it if it does not exist.
func (d *Database) GetOrCreateFolder(name string, syncEnabled bool) (*Folder, error) {
	query := "SELECT id, name, last_synced_uid, sync_enabled FROM folders WHERE name = ?;"
	var f Folder
	var syncVal any
	err := d.DB.QueryRow(query, name).Scan(&f.ID, &f.Name, &f.LastSyncedUID, &syncVal)
	if err == nil {
		f.SyncEnabled = toBool(syncVal)
		f.IsIMAP = !IsStandardFolder(f.Name)
		return &f, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	res, err := d.DB.Exec(
		"INSERT INTO folders (name, last_synced_uid, sync_enabled) VALUES (?, 0, ?);",
		name, syncEnabled,
	)
	if err != nil {
		// Retry select in case of concurrent insert
		err2 := d.DB.QueryRow(query, name).Scan(&f.ID, &f.Name, &f.LastSyncedUID, &syncVal)
		if err2 == nil {
			f.SyncEnabled = toBool(syncVal)
			f.IsIMAP = !IsStandardFolder(f.Name)
			return &f, nil
		}
		return nil, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	return &Folder{
		ID:            id,
		Name:          name,
		LastSyncedUID: 0,
		SyncEnabled:   syncEnabled,
		IsIMAP:        !IsStandardFolder(name),
	}, nil
}

// GetFolderByID retrieves a folder by its database ID.
func (d *Database) GetFolderByID(id int64) (*Folder, error) {
	query := "SELECT id, name, last_synced_uid, sync_enabled FROM folders WHERE id = ?;"
	var f Folder
	var syncVal any
	err := d.DB.QueryRow(query, id).Scan(&f.ID, &f.Name, &f.LastSyncedUID, &syncVal)
	if err != nil {
		return nil, err
	}
	f.SyncEnabled = toBool(syncVal)
	f.IsIMAP = !IsStandardFolder(f.Name)
	return &f, nil
}

// GetFolderByName retrieves a folder by name.
func (d *Database) GetFolderByName(name string) (*Folder, error) {
	query := "SELECT id, name, last_synced_uid, sync_enabled FROM folders WHERE name = ?;"
	var f Folder
	var syncVal any
	err := d.DB.QueryRow(query, name).Scan(&f.ID, &f.Name, &f.LastSyncedUID, &syncVal)
	if err != nil {
		return nil, err
	}
	f.SyncEnabled = toBool(syncVal)
	f.IsIMAP = !IsStandardFolder(f.Name)
	return &f, nil
}

// ListFolders lists all folders with unread and total message counts.
func (d *Database) ListFolders() ([]*Folder, error) {
	query := `
	SELECT
		f.id,
		f.name,
		f.last_synced_uid,
		f.sync_enabled,
		COUNT(m.id) AS total_count,
		COALESCE(SUM(CASE WHEN m.is_read = 0 THEN 1 ELSE 0 END), 0) AS unread_count
	FROM folders f
	LEFT JOIN messages m ON f.id = m.folder_id
	GROUP BY f.id, f.name, f.last_synced_uid, f.sync_enabled
	ORDER BY f.id ASC;
	`
	rows, err := d.DB.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query folders: %w", err)
	}
	defer rows.Close()

	var folders []*Folder
	for rows.Next() {
		var f Folder
		var syncVal any
		if err := rows.Scan(&f.ID, &f.Name, &f.LastSyncedUID, &syncVal, &f.TotalCount, &f.UnreadCount); err != nil {
			return nil, err
		}
		f.SyncEnabled = toBool(syncVal)
		f.IsIMAP = !IsStandardFolder(f.Name)
		folders = append(folders, &f)
	}
	return folders, nil
}

// UpdateFolderLastSyncedUID updates the highest IMAP UID seen for this folder.
func (d *Database) UpdateFolderLastSyncedUID(folderID int64, uid uint32) error {
	_, err := d.DB.Exec(
		"UPDATE folders SET last_synced_uid = MAX(last_synced_uid, ?) WHERE id = ?;",
		uid, folderID,
	)
	return err
}
