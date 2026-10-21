package commands

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

func TestExtractM3u8Text(t *testing.T) {
	got := extractM3u8Text(&tg.Message{Message: "check this https://cdn.example.com/live/abc.m3u8?token=1 done"})
	if got != "https://cdn.example.com/live/abc.m3u8?token=1" {
		t.Fatalf("got %q", got)
	}
	if extractM3u8Text(&tg.Message{Message: "https://x.com/i/status/123"}) != "" {
		t.Fatal("x link should not match")
	}
	if got := extractM3u8Text(&tg.Message{Message: "tap here", Entities: []tg.MessageEntityClass{&tg.MessageEntityTextURL{Offset: 0, Length: 3, URL: "https://cdn.example.com/v/hd.m3u8"}}}); got != "https://cdn.example.com/v/hd.m3u8" {
		t.Fatalf("entity URL not found: %q", got)
	}
}

func TestPickVariant(t *testing.T) {
	master := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360
low/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2400000,RESOLUTION=1280x720
hi/index.m3u8
`
	if got := pickVariant(master); got != "hi/index.m3u8" {
		t.Fatalf("got %q", got)
	}
	if got := pickVariant("#EXTM3U\n#EXTINF:4,\nseg1.ts\n"); got != "" {
		t.Fatalf("media playlist should return empty, got %q", got)
	}
}

func TestParseMediaPlaylist(t *testing.T) {
	base := "https://cdn.example.com/v/playlist.m3u8"
	content := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:10
#EXT-X-MEDIA-SEQUENCE:5
#EXT-X-KEY:METHOD=AES-128,URI="key.key",IV=0x00000000000000000000000000000001
#EXTINF:9.0,
seg-0.ts
#EXTINF:9.0,
seg-1.ts
#EXT-X-MAP:URI="init.mp4"
#EXT-X-KEY:METHOD=NONE
#EXTINF:9.0,
/media/absolute/seg-2.m4s
`
	segs, firstExt, mapURI := parseMediaPlaylist(content, base)
	if len(segs) != 3 {
		t.Fatalf("want 3 segs, got %d", len(segs))
	}
	if firstExt != ".ts" {
		t.Fatalf("firstExt=%q", firstExt)
	}
	if mapURI != "https://cdn.example.com/v/init.mp4" {
		t.Fatalf("mapURI=%q", mapURI)
	}
	if segs[0].uri != "https://cdn.example.com/v/seg-0.ts" || segs[0].seq != 5 {
		t.Fatalf("seg0=%+v", segs[0])
	}
	if segs[0].key == nil || len(segs[0].key.iv) != 16 {
		t.Fatalf("seg0 key=%+v", segs[0].key)
	}
	if segs[2].uri != "https://cdn.example.com/media/absolute/seg-2.m4s" {
		t.Fatalf("seg2=%q", segs[2].uri)
	}
	if segs[2].key != nil {
		t.Fatalf("seg2 should have no key, got %+v", segs[2].key)
	}
}

func TestM3u8Name(t *testing.T) {
	if n := m3u8Name("https://cdn.example.com/hls/%E7%94%B5%E5%BD%B1/index.m3u8?x=1"); !strings.HasPrefix(n, "index") && n == "" {
		t.Fatalf("n=%q", n)
	}
	if n := m3u8Name("https://cdn.example.com/hls/movie.m3u8"); n != "movie" {
		t.Fatalf("n=%q", n)
	}
}

func TestPkcs7Unpad(t *testing.T) {
	padded := append([]byte("hello"), 3, 3, 3)
	if got := pkcs7Unpad(padded); string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestCdFilename(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Disposition", `attachment; filename="movie 2026.ts"`)
	if got := cdFilename(h); got != "movie 2026" {
		t.Fatalf("got %q", got)
	}
	h2 := http.Header{}
	h2.Set("Content-Disposition", "attachment; filename=%E7%94%B5%E5%BD%B1.m3u8")
	if got := cdFilename(h2); got != "电影" {
		t.Fatalf("got %q", got)
	}
	if got := cdFilename(http.Header{}); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestDownloadM3u8Live(t *testing.T) {
	if os.Getenv("M3U8_LIVE_TEST") == "" {
		t.Skip("set M3U8_LIVE_TEST=1 to run")
	}
	out, err := downloadM3u8("https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8", t.TempDir(), "live")
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(out)
	if err != nil || st.Size() < 1024*1024 {
		t.Fatalf("output too small: %v %v", out, err)
	}
	t.Logf("live OK: %s (%d bytes)", out, st.Size())
}
