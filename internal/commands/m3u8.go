package commands

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/ext"
	"github.com/gotd/td/tg"
)

var m3u8Regex = regexp.MustCompile(`(?i)https?://[^\s]+\.m3u8(?:\?[^\s]*)?`)

var (
	bandwidthRe  = regexp.MustCompile(`BANDWIDTH=(\d+)`)
	keyMethodRe  = regexp.MustCompile(`METHOD=([A-Za-z0-9-]+)`)
	keyURIRe     = regexp.MustCompile(`URI="([^"]+)"`)
	keyIVRe      = regexp.MustCompile(`IV=0[xX]([0-9a-fA-F]+)`)
	mapURIRe     = regexp.MustCompile(`URI="([^"]+)"`)
	mediaSeqRe   = regexp.MustCompile(`#EXT-X-MEDIA-SEQUENCE:(\d+)`)
	byterangeRe  = regexp.MustCompile(`(?m)^#EXT-X-BYTERANGE`)
)

var m3u8HTTP = &http.Client{}

func extractM3u8Text(msg *tg.Message) string {
	text := msg.Message
	for _, ent := range msg.Entities {
		if ue, ok := ent.(*tg.MessageEntityTextURL); ok {
			text = text + " " + ue.URL
		}
	}
	return m3u8Regex.FindString(text)
}

type m3u8Key struct {
	method string
	uri    string
	iv     []byte
}

type m3uSeg struct {
	uri string
	key *m3u8Key
	seq int64
}

func httpGetWithHeader(u string, timeout time.Duration) ([]byte, http.Header, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := m3u8HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, u)
	}
	b, err := io.ReadAll(resp.Body)
	return b, resp.Header, err
}

func httpGetBytes(u string, timeout time.Duration) ([]byte, error) {
	b, _, err := httpGetWithHeader(u, timeout)
	return b, err
}

var cdFileRe = regexp.MustCompile(`filename\*?=(?:UTF-8''|")?([^";]+)`)

func cdFilename(h http.Header) string {
	cd := h.Get("Content-Disposition")
	if cd == "" {
		return ""
	}
	m := cdFileRe.FindStringSubmatch(cd)
	if m == nil {
		return ""
	}
	f, err := url.QueryUnescape(strings.Trim(m[1], `"`))
	if err != nil {
		f = m[1]
	}
	if c := cleanFileName(strings.TrimSuffix(f, path.Ext(f)), 50); c != "" {
		return c
	}
	return ""
}

func playlistBody(u string) (string, http.Header, error) {
	b, h, err := httpGetWithHeader(u, 30*time.Second)
	if err != nil {
		return "", nil, err
	}
	return string(b), h, nil
}

// resolveRef resolves ref (possibly relative) against base playlist URL.
func resolveRef(base, ref string) string {
	ru, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	if ru.IsAbs() {
		return ref
	}
	bu, err := url.Parse(base)
	if err != nil {
		return ref
	}
	return bu.ResolveReference(ru).String()
}

// pickVariant returns the highest-bandwidth variant URI of a master playlist, "" if already a media playlist.
func pickVariant(content string) string {
	if !strings.Contains(content, "#EXT-X-STREAM-INF") {
		return "" // media playlist, nothing to pick
	}
	best, bestBW := "", -1
	bw := 0
	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF") {
			bw = 0
			if m := bandwidthRe.FindStringSubmatch(line); m != nil {
				bw, _ = strconv.Atoi(m[1])
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if bw > bestBW {
			bestBW, best = bw, line
		}
		bw = 0
	}
	return best
}

func parseKeyLine(line, base string) (*m3u8Key, error) {
	method := ""
	if m := keyMethodRe.FindStringSubmatch(line); m != nil {
		method = m[1]
	}
	if method == "" || method == "NONE" {
		return nil, nil
	}
	if method != "AES-128" {
		return nil, fmt.Errorf("unsupported HLS encryption: %s", method)
	}
	k := &m3u8Key{method: method}
	if m := keyURIRe.FindStringSubmatch(line); m != nil {
		k.uri = resolveRef(base, m[1])
	}
	if m := keyIVRe.FindStringSubmatch(line); m != nil {
		k.iv, _ = hex.DecodeString(m[1])
	}
	return k, nil
}

func parseMediaPlaylist(content, base string) (segs []m3uSeg, firstExt string, mapURI string) {
	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	var curKey *m3u8Key
	seq := int64(0)
	if m := mediaSeqRe.FindStringSubmatch(content); m != nil {
		seq, _ = strconv.ParseInt(m[1], 10, 64)
	}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "#EXT-X-KEY"):
			if k, err := parseKeyLine(line, base); err == nil {
				curKey = k
			}
		case strings.HasPrefix(line, "#EXT-X-MAP"):
			if m := mapURIRe.FindStringSubmatch(line); m != nil {
				mapURI = resolveRef(base, m[1])
			}
		case line != "" && !strings.HasPrefix(line, "#"):
			if len(segs) >= 20000 {
				return
			}
			segs = append(segs, m3uSeg{uri: resolveRef(base, line), key: curKey, seq: seq})
			if firstExt == "" {
				e := path.Ext(line)
				if q := strings.IndexByte(e, '?'); q >= 0 {
					e = e[:q]
				}
				firstExt = strings.ToLower(e)
			}
			seq++
		}
	}
	return
}

func pkcs7Unpad(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(data) {
		return data
	}
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return data
		}
	}
	return data[:len(data)-pad]
}

func segmentData(seg m3uSeg) ([]byte, error) {
	var data []byte
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		data, err = httpGetBytes(seg.uri, 5*time.Minute)
		if err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("segment %s: %w", seg.uri, err)
	}
	if seg.key == nil {
		return data, nil
	}
	keyData, err := httpGetBytes(seg.key.uri, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("key %s: %w", seg.key.uri, err)
	}
	if len(keyData) != 16 {
		return nil, fmt.Errorf("bad key length %d", len(keyData))
	}
	iv := seg.key.iv
	if len(iv) == 0 {
		iv = make([]byte, 16)
		binary.BigEndian.PutUint64(iv[8:], uint64(seg.seq))
	}
	block, err := aes.NewCipher(keyData)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("segment %s: bad ciphertext length %d", seg.uri, len(data))
	}
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(data, data)
	return pkcs7Unpad(data), nil
}

func m3u8Name(rawURL string) string {
	name := "m3u8"
	if u, err := url.Parse(rawURL); err == nil {
		name = path.Base(u.Path)
	}
	name = strings.TrimSuffix(name, path.Ext(name))
	if dec, err := url.QueryUnescape(name); err == nil {
		name = dec
	}
	if c := cleanFileName(name, 50); c != "" {
		return c
	}
	return fmt.Sprintf("m3u8-%d", time.Now().Unix())
}

// ponytail: sequential segment download + no resume — router is bandwidth-bound;
// add ranged resume if long downloads ever get interrupted regularly.
func downloadM3u8(playlistURL, dir, name string) (string, error) {
	content, hdr, err := playlistBody(playlistURL)
	if err != nil {
		return "", fmt.Errorf("fetch m3u8: %w", err)
	}
	for i := 0; i < 2; i++ { // unwrap master playlist, at most 2 levels
		variant := pickVariant(content)
		if variant == "" {
			break
		}
		playlistURL = resolveRef(playlistURL, variant)
		if content, hdr, err = playlistBody(playlistURL); err != nil {
			return "", fmt.Errorf("fetch variant: %w", err)
		}
	}
	if name == "" { // priority: server Content-Disposition > URL path > timestamp
		if n := cdFilename(hdr); n != "" {
			name = n
		} else if n = m3u8Name(playlistURL); n != "" {
			name = n
		} else {
			name = fmt.Sprintf("m3u8-%d", time.Now().Unix())
		}
	}
	if byterangeRe.MatchString(content) {
		return "", fmt.Errorf("BYTERANGE playlists not supported")
	}
	segs, firstExt, mapURI := parseMediaPlaylist(content, playlistURL)
	if len(segs) == 0 {
		return "", fmt.Errorf("no segments in playlist")
	}
	ext := ".ts"
	if firstExt == ".m4s" || firstExt == ".mp4" || firstExt == ".cmf" {
		ext = ".mp4"
	}
	outPath := filepath.Join(dir, name+ext)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	out, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if mapURI != "" {
		initData, err := httpGetBytes(mapURI, 60*time.Second)
		if err != nil {
			return "", fmt.Errorf("init segment: %w", err)
		}
		if _, err := out.Write(initData); err != nil {
			return "", err
		}
	}
	for i, seg := range segs {
		data, err := segmentData(seg)
		if err != nil {
			return "", fmt.Errorf("seg %d/%d: %w", i+1, len(segs), err)
		}
		if _, err := out.Write(data); err != nil {
			return "", err
		}
	}
	return outPath, nil
}

func handleM3u8Link(ctx *ext.Context, u *ext.Update, rawURL string) {
	_, _ = ctx.Reply(u, ext.ReplyTextString("⏬ m3u8 download started"), nil)
	// NOTE: the goroutine outlives the handler and reuses ctx. This is fine for
	// gotgproto in practice; move the completion notify to a queue if it races.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				utils.Logger.Sugar().Errorf("m3u8 download panic: %v", r)
				_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("❌ m3u8 download failed: %v", r)), nil)
			}
		}()
		out, err := downloadM3u8(rawURL, config.ValueOf.DownloadDir, "")
		if err != nil {
			utils.Logger.Sugar().Errorf("m3u8 download failed: %v", err)
			_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("❌ m3u8 download failed: %v", err)), nil)
			return
		}
		utils.Logger.Sugar().Infof("m3u8 downloaded: %s", out)
		_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("✅ m3u8 saved: %s", out)), nil)
	}()
}
