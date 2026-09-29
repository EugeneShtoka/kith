package media_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/media"
)

// A cached file is named so an image viewer knows what it is holding.
func TestCachedFilesCarryTheirExtension(t *testing.T) {
	t.Parallel()

	cache := newCache(t)
	for _, tt := range []struct {
		name       string
		file, mime string
		want       string
	}{
		{"from the sender's filename", "holiday.JPEG", "", ".jpeg"},
		{"from the mime type when there is no filename", "", "image/png", ".png"},
		{"a jpeg is called .jpg, not the .jfif a mime table lists first", "", "image/jpeg", ".jpg"},
		{"and other types keep their only spelling", "", "image/webp", ".webp"},
		{"the filename wins over the mime type", "scan.png", "image/jpeg", ".png"},
		{"neither", "", "", ".bin"},
		{"a filename that is not really an extension", "no-dot-here", "", ".bin"},
		{"a filename that is a path", "../../.ssh/authorized_keys", "", ".bin"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := cache.Path("$event:x", tt.file, tt.mime)
			if filepath.Ext(got) != tt.want {
				t.Errorf("Path(%q, %q) = %q, want it to end in %q", tt.file, tt.mime, got, tt.want)
			}
			// Whatever a sender called their file, it does not get to name a path.
			if dir := filepath.Dir(got); dir != cache.Dir() {
				t.Errorf("the cached file landed in %q, outside the cache at %q", dir, cache.Dir())
			}
		})
	}
}

// What went in comes back out, and a miss is a miss rather than an empty picture.
func TestReadAfterWrite(t *testing.T) {
	t.Parallel()

	cache := newCache(t)
	if _, ok := cache.Read("$a:x", "a.png", "image/png"); ok {
		t.Error("an empty cache reported a hit")
	}
	want := []byte("some bytes")
	if _, err := cache.Write("$a:x", "a.png", "image/png", want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, ok := cache.Read("$a:x", "a.png", "image/png")
	if !ok || !bytes.Equal(got, want) {
		t.Errorf("Read = %q, %v; want %q", got, ok, want)
	}
}

// A render is kept per cell box, because a picture drawn forty cells wide and the same
// picture drawn twenty cells wide are different images.
func TestRendersAreKeptPerCellBox(t *testing.T) {
	t.Parallel()

	cache := newCache(t)
	if err := cache.PutRender("$a:x", 40, 10, []byte("wide")); err != nil {
		t.Fatalf("PutRender: %v", err)
	}
	if got, ok := cache.Render("$a:x", 40, 10); !ok || string(got) != "wide" {
		t.Errorf("Render(40x10) = %q, %v; want the wide one", got, ok)
	}
	if _, ok := cache.Render("$a:x", 20, 5); ok {
		t.Error("a render made for one cell box was handed back for another")
	}
	if _, ok := cache.Render("$b:x", 40, 10); ok {
		t.Error("one message's render was handed back for another message")
	}
}

// The cache root holds attachments and nothing else, so an image viewer can be pointed
// straight at it.
func TestTheCacheRootHoldsOnlyPictures(t *testing.T) {
	t.Parallel()

	cache := newCache(t)
	if _, err := cache.Write("$a:x", "a.jpg", "image/jpeg", []byte("bytes")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := cache.PutRender("$a:x", 20, 8, []byte("rows")); err != nil {
		t.Fatalf("PutRender: %v", err)
	}
	entries, err := os.ReadDir(cache.Dir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue // renders/
		}
		if filepath.Ext(e.Name()) != ".jpg" {
			t.Errorf("the cache root holds %q, which is not an attachment", e.Name())
		}
	}
	// And the drawing is still findable where it belongs.
	if _, ok := cache.Render("$a:x", 20, 8); !ok {
		t.Error("the drawing was not kept")
	}
}

// Over its limit, the cache drops what has been looked at least recently — not what was
// written least recently.
func TestTrimDropsTheLeastRecentlyUsed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cache, err := media.New(dir, 3000)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	kib := make([]byte, 1024)
	for _, event := range []string{"$old:x", "$mid:x", "$new:x"} {
		if _, werr := cache.Write(domain.EventID(event), "a.png", "image/png", kib); werr != nil {
			t.Fatalf("Write %s: %v", event, werr)
		}
	}
	// Make the ages unambiguous, oldest first, then use the oldest one.
	stamp(t, cache.Path("$old:x", "a.png", "image/png"), 1)
	stamp(t, cache.Path("$mid:x", "a.png", "image/png"), 2)
	stamp(t, cache.Path("$new:x", "a.png", "image/png"), 3)
	cache.Touch(cache.Path("$old:x", "a.png", "image/png")) // looked at just now

	if _, werr := cache.Write("$extra:x", "a.png", "image/png", kib); werr != nil {
		t.Fatalf("Write extra: %v", werr)
	}
	freed, err := cache.Trim()
	if err != nil {
		t.Fatalf("Trim: %v", err)
	}
	if freed == 0 {
		t.Fatal("Trim freed nothing from a cache over its limit")
	}
	if _, ok := cache.Read("$old:x", "a.png", "image/png"); !ok {
		t.Error("Trim dropped the file that had just been looked at")
	}
	if _, ok := cache.Read("$mid:x", "a.png", "image/png"); ok {
		t.Error("Trim kept the least recently used file")
	}
}

// A cache under its limit is left entirely alone.
func TestTrimLeavesASmallCacheAlone(t *testing.T) {
	t.Parallel()

	cache := newCache(t)
	if _, err := cache.Write("$a:x", "a.png", "image/png", []byte("small")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if freed, err := cache.Trim(); err != nil || freed != 0 {
		t.Errorf("Trim = %d, %v; want it to leave a small cache alone", freed, err)
	}
	if _, ok := cache.Read("$a:x", "a.png", "image/png"); !ok {
		t.Error("Trim dropped a file from a cache that was under its limit")
	}
}

// A nil cache is a working client that simply does the work every time, so no call site
// needs a branch around it.
func TestNilCacheIsUsable(t *testing.T) {
	t.Parallel()

	var cache *media.Cache
	if _, ok := cache.Read("$a:x", "a.png", "image/png"); ok {
		t.Error("a nil cache reported a hit")
	}
	if _, ok := cache.Render("$a:x", 4, 4); ok {
		t.Error("a nil cache reported a render")
	}
	if path, err := cache.Write("$a:x", "a.png", "image/png", []byte("x")); err != nil || path != "" {
		t.Errorf("writing to a nil cache = %q, %v; want it to do nothing quietly", path, err)
	}
	if err := cache.PutRender("$a:x", 4, 4, []byte("x")); err != nil {
		t.Errorf("putting a render in a nil cache: %v", err)
	}
	if freed, err := cache.Trim(); err != nil || freed != 0 {
		t.Errorf("trimming a nil cache = %d, %v", freed, err)
	}
	cache.Touch("/nowhere")
}

// Nothing half-written is ever left where a reader could find it: a torn PNG is worse
// than a miss, because a miss is retried and a torn file is believed.
func TestWritesLeaveNoPartialFiles(t *testing.T) {
	t.Parallel()

	cache := newCache(t)
	if _, err := cache.Write("$a:x", "a.png", "image/png", []byte("bytes")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	entries, err := os.ReadDir(cache.Dir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

// testEpoch and testStep give the trim test unambiguous, ordered file ages.
var (
	testEpoch = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	testStep  = time.Hour
)

func newCache(t *testing.T) *media.Cache {
	t.Helper()

	cache, err := media.New(t.TempDir(), -1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return cache
}

// stamp gives a file a distinct, ordered modification time.
func stamp(t *testing.T, path string, age int) {
	t.Helper()

	when := testEpoch.Add(testStep * time.Duration(age))
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("Chtimes %s: %v", path, err)
	}
}
