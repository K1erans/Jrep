package cache

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	staleAfter    = 30 * 24 * time.Hour
	cleanupEvery  = 24 * time.Hour
	touchAfter    = 24 * time.Hour // last_used is refreshed at most this often
	lookupBatch   = 500
	busyTimeoutMS = 10000
)

// now is the clock; tests replace it.
var now = time.Now

const schema = `
CREATE TABLE IF NOT EXISTS answers (
	key       BLOB PRIMARY KEY,
	noul      REAL NOT NULL,
	model     TEXT NOT NULL,
	last_used INTEGER NOT NULL
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS meta (
	name  TEXT PRIMARY KEY,
	value INTEGER NOT NULL
);`

// SQLite is the on-disk Cache. WAL mode, a busy timeout and INSERT OR
// IGNORE let several jrep processes share one database.
type SQLite struct {
	db    *sql.DB
	model string
}

// Dir is the folder holding cache databases: $JREP_CACHE_DIR, or jrep
// inside the user cache folder (~/Library/Caches/jrep on macOS).
func Dir() (string, error) {
	if d := os.Getenv("JREP_CACHE_DIR"); d != "" {
		return d, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "jrep"), nil
}

// Path is the database file for the repo at root.
func Path(dir, root string) string {
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".db")
}

// Clear deletes the database at path and its WAL files.
func Clear(path string) error {
	var errs []error
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Open opens or creates the database at path. model is the version being
// searched with; once a day, rows from other versions and rows unused for
// 30 days are deleted.
func Open(path, model string) (*SQLite, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate",
		path, busyTimeoutMS)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection per process: writers queue here instead of contending
	// for SQLite's lock.
	db.SetMaxOpenConns(1)
	c := &SQLite{db: db, model: model}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening cache %s: %w", path, err)
	}
	if err := c.cleanup(); err != nil {
		db.Close()
		return nil, fmt.Errorf("cleaning cache %s: %w", path, err)
	}
	return c, nil
}

func (c *SQLite) Close() error { return c.db.Close() }

func (c *SQLite) cleanup() error {
	t := now().Unix()
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var last int64
	err = tx.QueryRow(`SELECT value FROM meta WHERE name = 'last_cleanup'`).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && t-last < int64(cleanupEvery/time.Second) {
		return nil
	}
	if _, err := tx.Exec(`DELETE FROM answers WHERE last_used < ? OR model != ?`, t-int64(staleAfter/time.Second), c.model); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO meta (name, value) VALUES ('last_cleanup', ?)`, t); err != nil {
		return err
	}
	return tx.Commit()
}

// Get returns the cached probability for every key it holds, and marks
// those rows as used.
func (c *SQLite) Get(keys []Key) (map[Key]float64, error) {
	out := make(map[Key]float64, len(keys))
	t := now().Unix()
	for len(keys) > 0 {
		batch := keys[:min(lookupBatch, len(keys))]
		keys = keys[len(batch):]
		args := make([]any, len(batch))
		for i, k := range batch {
			args[i] = k[:]
		}
		in := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		rows, err := c.db.Query(`SELECT key, noul, last_used FROM answers WHERE key IN (`+in+`)`, args...)
		if err != nil {
			return nil, err
		}
		var stale []any
		for rows.Next() {
			var raw []byte
			var noul float64
			var lastUsed int64
			if err := rows.Scan(&raw, &noul, &lastUsed); err != nil {
				rows.Close()
				return nil, err
			}
			var k Key
			copy(k[:], raw)
			out[k] = noul
			if t-lastUsed > int64(touchAfter/time.Second) {
				stale = append(stale, raw)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(stale) > 0 {
			in := strings.TrimSuffix(strings.Repeat("?,", len(stale)), ",")
			if _, err := c.db.Exec(`UPDATE answers SET last_used = ? WHERE key IN (`+in+`)`, append([]any{t}, stale...)...); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// Put saves one answer. An existing answer for the key is kept: the key
// already pins the exact model, question and code.
func (c *SQLite) Put(key Key, noul float64) error {
	_, err := c.db.Exec(`INSERT OR IGNORE INTO answers (key, noul, model, last_used) VALUES (?, ?, ?, ?)`,
		key[:], noul, c.model, now().Unix())
	return err
}
