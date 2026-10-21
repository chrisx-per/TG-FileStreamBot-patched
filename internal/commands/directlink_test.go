package commands

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestUrlExt(t *testing.T) {
	cases := map[string]string{
		"https://cdn.example.com/a/b.MP4":         ".mp4",
		"https://cdn.example.com/a/b.mkv?token=1": ".mkv",
		"https://cdn.example.com/a/b":             "",
		"https://cdn.example.com/list.m3u8?x=1":   ".m3u8",
		"https://cdn.example.com/a/file.tar.gz":   ".gz",
	}
	for in, want := range cases {
		if got := urlExt(in); got != want {
			t.Errorf("urlExt(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractDirectURL(t *testing.T) {
	tests := []struct {
		name string
		msg  *tg.Message
		want string
	}{
		{"mp4", &tg.Message{Message: "grab https://cdn.example.com/v/clip.mp4 please"}, "https://cdn.example.com/v/clip.mp4"},
		{"query kept", &tg.Message{Message: "https://cdn.example.com/v/clip.mkv?token=abc"}, "https://cdn.example.com/v/clip.mkv?token=abc"},
		{"m3u8 skipped", &tg.Message{Message: "https://cdn.example.com/live/hd.m3u8"}, ""},
		{"x link skipped", &tg.Message{Message: "https://x.com/i/status/123"}, ""},
		{"html page skipped", &tg.Message{Message: "https://example.com/article?id=1"}, ""},
		{"plain text", &tg.Message{Message: "no links here"}, ""},
		{"entity url", &tg.Message{
			Message:  "download",
			Entities: []tg.MessageEntityClass{&tg.MessageEntityTextURL{Offset: 0, Length: 8, URL: "https://cdn.example.com/a/song.mp3"}},
		}, "https://cdn.example.com/a/song.mp3"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractDirectURL(tc.msg); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDirectLinkName(t *testing.T) {
	cases := map[string]string{
		"https://cdn.example.com/v/clip.mp4":               "clip.mp4",
		"https://cdn.example.com/v/clip.mp4?token=1":       "clip.mp4",
		"https://cdn.example.com/v/%E7%94%B5%E5%BD%B1.mkv": "\u7535\u5f71.mkv",
		"https://cdn.example.com/":                         "",
	}
	for in, want := range cases {
		got := directLinkName(in)
		if want == "" {
			// falls back to a timestamp-based name
			if len(got) < 9 || got[:9] != "download-" {
				t.Errorf("directLinkName(%q) = %q, want download-* fallback", in, got)
			}
			continue
		}
		if got != want {
			t.Errorf("directLinkName(%q) = %q, want %q", in, got, want)
		}
	}
}
