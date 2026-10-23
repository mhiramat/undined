package database

import (
	"database/sql"
	"strings"

	"undined-go/internal/storage/parser"
)

// KnownAddress represents an email address and display name tracked from past emails.
type KnownAddress struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Address   string `json:"address"`
	Frequency int    `json:"frequency"`
	LastSeen  string `json:"last_seen"`
	CreatedAt string `json:"created_at"`
}

// RecordKnownAddress inserts or increments the frequency of a known address.
func (d *Database) RecordKnownAddress(name, address string) error {
	address = strings.ToLower(strings.TrimSpace(address))
	address = strings.Trim(address, "<>")
	if address == "" || !strings.Contains(address, "@") {
		return nil
	}
	name = strings.TrimSpace(name)
	name = strings.Trim(name, `"'`)

	query := `
	INSERT INTO known_addresses (name, address, frequency, last_seen)
	VALUES (?, ?, 1, CURRENT_TIMESTAMP)
	ON CONFLICT(address) DO UPDATE SET
		frequency = known_addresses.frequency + 1,
		last_seen = CURRENT_TIMESTAMP,
		name = CASE WHEN excluded.name != '' THEN excluded.name ELSE known_addresses.name END;
	`
	_, err := d.DB.Exec(query, name, address)
	return err
}

// RecordKnownAddressesFromHeaders extracts addresses from To/Cc header strings and upserts them.
func (d *Database) RecordKnownAddressesFromHeaders(headers ...string) error {
	addrs := parser.ExtractAddresses(headers...)
	if len(addrs) == 0 {
		return nil
	}

	tx, err := d.DB.Begin()
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

	for _, a := range addrs {
		if _, err := stmt.Exec(a.Name, a.Address); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// CollectKnownAddresses scans all messages in the messages table, extracts To/Cc addresses,
// and upserts them into known_addresses. Returns the number of address records processed.
func (d *Database) CollectKnownAddresses() (int, error) {
	rows, err := d.DB.Query("SELECT to_addr, cc_addr FROM messages WHERE (to_addr IS NOT NULL AND to_addr != '') OR (cc_addr IS NOT NULL AND cc_addr != '');")
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	tx, err := d.DB.Begin()
	if err != nil {
		return 0, err
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
		return 0, err
	}
	defer stmt.Close()

	var count int
	for rows.Next() {
		var toAddr, ccAddr sql.NullString
		if err := rows.Scan(&toAddr, &ccAddr); err != nil {
			continue
		}
		addrs := parser.ExtractAddresses(toAddr.String, ccAddr.String)
		for _, a := range addrs {
			if _, err := stmt.Exec(a.Name, a.Address); err == nil {
				count++
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

// SearchKnownAddresses queries the known_addresses table matching query against name or address.
// Results are ranked with prefix matches first, followed by frequency and last seen timestamp.
func (d *Database) SearchKnownAddresses(query string, limit int) ([]*KnownAddress, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	query = strings.TrimSpace(query)

	var sqlQuery string
	var args []any

	if query == "" {
		sqlQuery = `
		SELECT id, name, address, frequency, last_seen, created_at
		FROM known_addresses
		ORDER BY frequency DESC, last_seen DESC
		LIMIT ?;
		`
		args = []any{limit}
	} else {
		escaped := escapeLikePattern(query)
		likePattern := "%" + escaped + "%"
		prefixPattern := escaped + "%"
		sqlQuery = `
		SELECT id, name, address, frequency, last_seen, created_at
		FROM known_addresses
		WHERE address LIKE ? ESCAPE '\' OR name LIKE ? ESCAPE '\'
		ORDER BY
			CASE
				WHEN address LIKE ? ESCAPE '\' THEN 1
				WHEN name LIKE ? ESCAPE '\' THEN 2
				ELSE 3
			END,
			frequency DESC,
			last_seen DESC
		LIMIT ?;
		`
		args = []any{likePattern, likePattern, prefixPattern, prefixPattern, limit}
	}

	rows, err := d.DB.Query(sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*KnownAddress
	for rows.Next() {
		var ka KnownAddress
		if err := rows.Scan(&ka.ID, &ka.Name, &ka.Address, &ka.Frequency, &ka.LastSeen, &ka.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, &ka)
	}
	if results == nil {
		results = make([]*KnownAddress, 0)
	}
	return results, nil
}

// GetKnownAddressCount returns the total number of unique known addresses.
func (d *Database) GetKnownAddressCount() (int, error) {
	var count int
	err := d.DB.QueryRow("SELECT COUNT(*) FROM known_addresses;").Scan(&count)
	return count, err
}

// DeleteKnownAddress removes a specific address record by ID.
func (d *Database) DeleteKnownAddress(id int64) error {
	_, err := d.DB.Exec("DELETE FROM known_addresses WHERE id = ?;", id)
	return err
}
