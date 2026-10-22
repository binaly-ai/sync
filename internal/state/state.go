// Package state is Binaly Sync's local memory, in one bbolt file:
//
//   - the hash cache: path -> size, mtime, sha256. A file is re-hashed only when its size or
//     modification time changed, and hashed once however many workspaces sync it.
//   - per workspace and folder, what that workspace was last told about each path. Comparing
//     a walk against it gives new, changed and deleted files, so a normal run reports only
//     changes.
//
// Losing this file costs a re-hash and one full report; nothing on the server is harmed.
package state

import (
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketHashes = []byte("hashes")
	bucketMeta   = []byte("meta")
)

// Hash is a cached hash, valid while size and mtime are unchanged.
type Hash struct {
	Size    int64  `json:"s"`
	MtimeNs int64  `json:"m"`
	SHA256  string `json:"h"`
}

// Reported is what one workspace was told about one path.
type Reported struct {
	Path    string `json:"p"`
	Size    int64  `json:"s"`
	MtimeNs int64  `json:"m"`
	SHA256  string `json:"h"`
	// Uploaded is false while the server still wants the bytes; such paths are re-sent.
	Uploaded bool `json:"u"`
}

// DB is the open state file.
type DB struct{ db *bolt.DB }

// Open opens (or creates) the state file. Only one process may hold it, which doubles as the
// single-instance lock: two runs can't rotate the same refresh token at once.
func Open(path string) (*DB, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketHashes, bucketMeta} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	return &DB{db}, err
}

// Close closes the file.
func (d *DB) Close() error { return d.db.Close() }

// CachedHash returns the cached hash for a path if size and mtime still match.
func (d *DB) CachedHash(key string, size, mtimeNs int64) (string, bool) {
	var h Hash
	ok := false
	_ = d.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketHashes).Get([]byte(key))
		if v != nil && json.Unmarshal(v, &h) == nil && h.Size == size && h.MtimeNs == mtimeNs {
			ok = true
		}
		return nil
	})
	return h.SHA256, ok
}

// PutHashes stores freshly computed hashes.
func (d *DB) PutHashes(entries map[string]Hash) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketHashes)
		for k, h := range entries {
			v, _ := json.Marshal(h)
			if err := b.Put([]byte(k), v); err != nil {
				return err
			}
		}
		return nil
	})
}

func reportedBucket(account, folderID string) []byte {
	return []byte("reported|" + account + "|" + folderID)
}

// AllReported loads what a workspace was told about a folder, keyed by path key.
func (d *DB) AllReported(account, folderID string) (map[string]Reported, error) {
	out := map[string]Reported{}
	err := d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(reportedBucket(account, folderID))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var r Reported
			if json.Unmarshal(v, &r) == nil {
				out[string(k)] = r
			}
			return nil
		})
	})
	return out, err
}

// HasReported says whether this workspace was ever told anything about the folder.
func (d *DB) HasReported(account, folderID string) bool {
	has := false
	_ = d.db.View(func(tx *bolt.Tx) error {
		has = tx.Bucket(reportedBucket(account, folderID)) != nil
		return nil
	})
	return has
}

// PutReported records what the workspace now knows (an empty map still marks the folder known).
func (d *DB) PutReported(account, folderID string, entries map[string]Reported) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(reportedBucket(account, folderID))
		if err != nil {
			return err
		}
		for k, r := range entries {
			v, _ := json.Marshal(r)
			if err := b.Put([]byte(k), v); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteReported forgets paths the workspace was told are gone.
func (d *DB) DeleteReported(account, folderID string, keys []string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(reportedBucket(account, folderID))
		if b == nil {
			return nil
		}
		for _, k := range keys {
			if err := b.Delete([]byte(k)); err != nil {
				return err
			}
		}
		return nil
	})
}

// DropFolder forgets everything about a folder for one workspace (it stopped syncing it).
func (d *DB) DropFolder(account, folderID string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		err := tx.DeleteBucket(reportedBucket(account, folderID))
		if err == bolt.ErrBucketNotFound {
			return nil
		}
		return err
	})
}

// LastFull returns when this workspace last got a full scan of the folder.
func (d *DB) LastFull(account, folderID string) time.Time {
	var t time.Time
	_ = d.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(bucketMeta).Get([]byte("full|" + account + "|" + folderID)); v != nil {
			t, _ = time.Parse(time.RFC3339, string(v))
		}
		return nil
	})
	return t
}

// SetLastFull records a finished full scan.
func (d *DB) SetLastFull(account, folderID string, t time.Time) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketMeta).Put([]byte("full|"+account+"|"+folderID), []byte(t.UTC().Format(time.RFC3339)))
	})
}
