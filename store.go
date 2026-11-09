package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go: keeps the Pi build CGO-free
)

// The library index is kept in SQLite (library.db next to settings.json), so
// a restart serves the last scan at once instead of starting empty, and the
// periodic rescan resumes on schedule rather than at every start.
//
// Parsed titles aren't stored: they're recomputed on load, so parser fixes
// apply to old data too.

var db *sql.DB

// schemaVersion bumps whenever the tables change; library.db is only a cache
// of the last scan, so an old one is simply dropped and rebuilt.
const schemaVersion = 5

const schema = `
CREATE TABLE IF NOT EXISTS sources (
	id TEXT PRIMARY KEY, host TEXT, kind TEXT, name TEXT, addr TEXT,
	items INTEGER, error TEXT, stale INTEGER
);
CREATE TABLE IF NOT EXISTS items (
	key TEXT PRIMARY KEY, source_id TEXT, server TEXT, title TEXT, file_name TEXT,
	url TEXT, smb_host TEXT, smb_share TEXT, path TEXT, dav_root TEXT,
	sftp_addr TEXT, local_file TEXT, size INTEGER, duration TEXT, resolution TEXT, poster TEXT,
	kind TEXT, info_hash TEXT, file_idx INTEGER, trackers TEXT, entries TEXT, album TEXT
);
CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT);
CREATE TABLE IF NOT EXISTS dav_dirs (k TEXT PRIMARY KEY, stamp TEXT);`

func openDB(path string) error {
	d, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	d.SetMaxOpenConns(1) // one writer; reads happen only at startup
	var v int
	if err := d.QueryRow(`PRAGMA user_version`).Scan(&v); err == nil && v != schemaVersion {
		d.Exec(`DROP TABLE IF EXISTS sources; DROP TABLE IF EXISTS items; DROP TABLE IF EXISTS meta; DROP TABLE IF EXISTS dav_dirs;`)
	}
	if _, err := d.Exec(schema + fmt.Sprintf("PRAGMA user_version = %d;", schemaVersion)); err != nil {
		d.Close()
		return fmt.Errorf("%s: %w", path, err)
	}
	db = d
	return nil
}

// loadIndex returns the last saved index, or nil if there is none.
func loadIndex() (*Index, error) {
	if db == nil {
		return nil, nil
	}
	var built string
	if err := db.QueryRow(`SELECT v FROM meta WHERE k = 'built'`).Scan(&built); err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	idx := newIndex()
	idx.Built, _ = time.Parse(time.RFC3339Nano, built)

	rows, err := db.Query(`SELECT id, host, kind, name, addr, items, error, stale FROM sources ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s Source
		if err := rows.Scan(&s.ID, &s.Host, &s.Kind, &s.Name, &s.Addr, &s.Items, &s.Error, &s.Stale); err != nil {
			rows.Close()
			return nil, err
		}
		idx.Sources = append(idx.Sources, s)
	}
	rows.Close()

	rows, err = db.Query(`SELECT key, source_id, server, title, file_name, url, smb_host, smb_share, path,
		dav_root, sftp_addr, local_file, size, duration, resolution, poster,
		kind, info_hash, file_idx, trackers, entries, album FROM items ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		it := &Item{}
		var trackers, entries string
		if err := rows.Scan(&it.Key, &it.SourceID, &it.Server, &it.Title, &it.FileName, &it.URL, &it.SMBHost,
			&it.SMBShare, &it.Path, &it.DAVRoot, &it.SFTPAddr, &it.LocalFile, &it.Size, &it.Duration,
			&it.Resolution, &it.Poster, &it.Kind, &it.InfoHash, &it.FileIdx, &trackers, &entries, &it.Album); err != nil {
			return nil, err
		}
		if trackers != "" {
			it.Trackers = strings.Split(trackers, "\n")
		}
		if entries != "" {
			json.Unmarshal([]byte(entries), &it.Entries)
		}
		idx.add(it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	stamps, err := db.Query(`SELECT k, stamp FROM dav_dirs`)
	if err != nil {
		return nil, err
	}
	defer stamps.Close()
	for stamps.Next() {
		var k, v string
		if err := stamps.Scan(&k, &v); err != nil {
			return nil, err
		}
		idx.DAVStamps[k] = v
	}
	idx.finish()
	return idx, stamps.Err()
}

// saveIndex replaces the stored index with idx in one transaction.
func saveIndex(idx *Index) error {
	if db == nil {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM sources`, `DELETE FROM items`, `DELETE FROM dav_dirs`} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	src, err := tx.Prepare(`INSERT INTO sources VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	for _, s := range idx.Sources {
		if _, err := src.Exec(s.ID, s.Host, s.Kind, s.Name, s.Addr, s.Items, s.Error, s.Stale); err != nil {
			return err
		}
	}
	item, err := tx.Prepare(`INSERT INTO items VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	for _, it := range idx.All {
		var entries []byte
		if len(it.Entries) > 0 {
			entries, _ = json.Marshal(it.Entries)
		}
		if _, err := item.Exec(it.Key, it.SourceID, it.Server, it.Title, it.FileName, it.URL, it.SMBHost,
			it.SMBShare, it.Path, it.DAVRoot, it.SFTPAddr, it.LocalFile, it.Size, it.Duration,
			it.Resolution, it.Poster, it.Kind, it.InfoHash, it.FileIdx, strings.Join(it.Trackers, "\n"),
			string(entries), it.Album); err != nil {
			return err
		}
	}
	stamp, err := tx.Prepare(`INSERT INTO dav_dirs VALUES (?, ?)`)
	if err != nil {
		return err
	}
	for k, v := range idx.DAVStamps {
		if _, err := stamp.Exec(k, v); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO meta VALUES ('built', ?)`, idx.Built.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

// loadLibrary opens library.db and serves its index until the first scan.
func loadLibrary(path string) {
	if err := openDB(path); err != nil {
		log.Printf("library db: %v (scans won't be saved)", err)
		return
	}
	idx, err := loadIndex()
	if err != nil {
		log.Printf("library db: %v (starting empty)", err)
		return
	}
	if idx != nil {
		current.Store(idx)
		log.Printf("library: %d items from %s, last scan %s ago", len(idx.All), path, time.Since(idx.Built).Round(time.Second))
	}
}
