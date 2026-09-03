package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A picture read once is on disk for the next screen, and the next run.
func TestThePictureCacheKeepsWhatWasRead(t *testing.T) {
	store := newPictureCache(filepath.Join(t.TempDir(), "pictures"))
	if store == nil {
		t.Fatal("the cache would not open")
	}

	const source = "https://3.basecampapi.com/1/blobs/abc/download/shot.webp"
	if got := store.read(source); got != nil {
		t.Errorf("an empty cache answered %d bytes", len(got))
	}

	store.write(source, []byte("pixels"))
	if got := string(store.read(source)); got != "pixels" {
		t.Errorf("the cache answered %q, want %q", got, "pixels")
	}
}

// The query string is where the signatures and the sizes live, and neither says
// which picture this is: the same blob asked for twice is one entry.
func TestThePictureCacheIgnoresTheQueryString(t *testing.T) {
	store := newPictureCache(filepath.Join(t.TempDir(), "pictures"))

	store.write("https://3.basecampapi.com/1/blobs/abc/download/shot.webp?signed=true", []byte("pixels"))
	if got := string(store.read("https://3.basecampapi.com/1/blobs/abc/download/shot.webp?dppx=2")); got != "pixels" {
		t.Errorf("the same blob under a different query answered %q", got)
	}
}

// An avatar's address carries a signed person id rather than a content hash, so
// the same address answers a new picture when somebody changes their photo.
// Stale entries are not shown and do not sit there.
func TestThePictureCacheDropsWhatHasGoneStale(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pictures")
	store := newPictureCache(dir)

	const source = "https://assets.basecamp-static.com/1/people/a/avatar"
	store.write(source, []byte("last year's face"))

	stale := time.Now().Add(-pictureCacheTTL - time.Hour)
	if err := os.Chtimes(store.pathFor(source), stale, stale); err != nil {
		t.Fatal(err)
	}
	if got := store.read(source); got != nil {
		t.Errorf("a stale picture was answered with %d bytes", len(got))
	}

	// Opening the store again is what clears them out, so a directory of
	// screenshots does not only grow.
	newPictureCache(dir)
	if _, err := os.Stat(store.pathFor(source)); !os.IsNotExist(err) {
		t.Error("a stale picture was left on disk")
	}
}

// A hit does not go to the wire, and a miss keeps what it fetched.
func TestTheCachedReaderOnlyFetchesOnce(t *testing.T) {
	store := newPictureCache(filepath.Join(t.TempDir(), "pictures"))

	reads := 0
	read := cached(store, func(context.Context, string, int64) ([]byte, error) {
		reads++
		return []byte("pixels"), nil
	})

	for range 3 {
		if _, err := read(context.Background(), "https://3.basecampapi.com/1/blobs/abc/download/a.png", 1<<20); err != nil {
			t.Fatal(err)
		}
	}
	if reads != 1 {
		t.Errorf("the picture went over the wire %d times, want once", reads)
	}
}

// A read that failed is not cached: the next screen should try again rather than
// inherit the failure.
func TestTheCachedReaderKeepsNothingFromAFailure(t *testing.T) {
	store := newPictureCache(filepath.Join(t.TempDir(), "pictures"))

	reads := 0
	read := cached(store, func(context.Context, string, int64) ([]byte, error) {
		reads++
		return nil, errors.New("nope")
	})

	const source = "https://3.basecampapi.com/1/blobs/abc/download/a.png"
	for range 2 {
		if _, err := read(context.Background(), source, 1<<20); err == nil {
			t.Fatal("a failed read answered without an error")
		}
	}
	if reads != 2 {
		t.Errorf("the failure was cached: %d reads, want 2", reads)
	}
}

// A cache with nowhere to write is a cache that misses, so nothing has to check
// for one.
func TestNoCacheIsACacheThatMisses(t *testing.T) {
	var none *pictureCache

	none.write("https://example.com/a.png", []byte("pixels"))
	if got := none.read("https://example.com/a.png"); got != nil {
		t.Errorf("a cache that does not exist answered %d bytes", len(got))
	}
	if newPictureCache("") != nil {
		t.Error("a cache opened with nowhere to put it")
	}
}
