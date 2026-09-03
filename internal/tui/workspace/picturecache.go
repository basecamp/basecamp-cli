package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/basecamp/basecamp-cli/internal/appctx"
)

// How long a picture on disk is still worth showing.
//
// The blobs never change — Basecamp addresses one by a uuid, so a URL that
// answers once answers the same bytes forever. An avatar does change: the URL
// carries a signed person id rather than a content address, so the same address
// answers a new picture when somebody changes their photo. A day is the
// compromise: nobody notices a blob refetched once a day, and nobody keeps
// showing last year's face.
const pictureCacheTTL = 24 * time.Hour

// pictureCache is what has already been read, kept between runs.
//
// A face is a dozen kilobytes and the same few faces are on every screen; a
// screenshot is megabytes and is looked at more than once. Neither is worth
// fetching twice, and without this every screen fetched its own copy — one per
// message, per card, per chat.
//
// It goes in the cache directory the rest of the CLI already uses, under a
// subdirectory of its own. Nothing is encrypted: the OAuth token sits in the
// config in plain text, so anybody who can read these files can fetch the same
// pictures themselves, and a key kept beside what it locks is not a lock. What
// does apply is the permissions the other stores use — 0700 on the directory,
// 0600 on the files.
type pictureCache struct{ dir string }

// pictureStore is the one store the whole workspace shares: the point of it is
// that a face read for one screen is already there for the next, so a store per
// screen would be no store at all.
//
// It is opened once, on the first read, and answers nil for good if there is
// nowhere to put it or the reader has turned caching off. A nil store is a store
// that misses, so no caller has to check.
func pictureStore(app *appctx.App) *pictureCache {
	openStore.Do(func() {
		if app.Config == nil || !app.Config.CacheEnabled {
			return
		}
		store = newPictureCache(filepath.Join(app.Config.CacheDir, "pictures"))
	})
	return store
}

var (
	openStore sync.Once
	store     *pictureCache
)

// newPictureCache opens the store at a directory, and answers nil when it
// cannot be written to.
func newPictureCache(dir string) *pictureCache {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil
	}

	opened := &pictureCache{dir: dir}
	opened.prune()
	return opened
}

// read answers the bytes on disk for a source, and nothing when they are not
// there or have gone stale.
func (c *pictureCache) read(source string) []byte {
	if c == nil {
		return nil
	}
	at := c.pathFor(source)

	found, err := os.Stat(at)
	if err != nil || time.Since(found.ModTime()) > pictureCacheTTL {
		return nil
	}
	data, err := os.ReadFile(at) //nolint:gosec // G304: path is a hash under our own cache dir
	if err != nil {
		return nil
	}
	return data
}

// write puts a picture on disk. A failure is not worth saying anything about: the
// picture is already on screen, and the only cost is fetching it again.
func (c *pictureCache) write(source string, data []byte) {
	if c == nil || len(data) == 0 {
		return
	}

	at := c.pathFor(source)
	tmp := at + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, at); err != nil {
		_ = os.Remove(tmp)
	}
}

// pathFor names a picture's file after its address with the query string taken
// off.
//
// The query is where the signatures and the sizes live — signed=true, dppx=2 —
// and neither says anything about which picture this is. The path does: a blob's
// uuid, or a person's signed id. Hashed rather than used as a filename, because
// a URL is not one.
func (c *pictureCache) pathFor(source string) string {
	stable := source
	if parsed, err := url.Parse(source); err == nil {
		parsed.RawQuery, parsed.Fragment = "", ""
		stable = parsed.String()
	}

	sum := sha256.Sum256([]byte(stable))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:]))
}

// prune throws out what has gone stale, once when the store opens. Without it a
// directory of screenshots only grows: the pictures a reader looked at a month
// ago are pictures nobody will ask for again.
func (c *pictureCache) prune() {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		found, err := entry.Info()
		if err != nil || time.Since(found.ModTime()) <= pictureCacheTTL {
			continue
		}
		_ = os.Remove(filepath.Join(c.dir, entry.Name()))
	}
}

// cached reads from disk before going to the wire, and keeps whatever it had to
// fetch.
//
// It wraps the reader rather than the callers, so it sits outside traced: the
// trace stays a record of what actually went over the wire, and a hit is silent
// because nothing happened.
func cached(store *pictureCache, read imageReader) imageReader {
	return func(ctx context.Context, source string, maxBytes int64) ([]byte, error) {
		if data := store.read(source); len(data) > 0 {
			return data, nil
		}

		data, err := read(ctx, source, maxBytes)
		if err != nil {
			return nil, err
		}
		store.write(source, data)
		return data, nil
	}
}
