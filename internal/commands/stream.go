package commands

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/storage"
	"github.com/celestix/gotgproto/types"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
)

var xTweetRegex = regexp.MustCompile(`https?://(?:www\.)?(?:x\.com|twitter\.com)/[^/]+/status/(\d+)`)

func (m *command) LoadStream(dispatcher dispatcher.Dispatcher) {
	log := m.log.Named("start")
	defer log.Sugar().Info("Loaded")
	dispatcher.AddHandler(
		handlers.NewMessage(nil, sendLink),
	)
}

func supportedMediaFilter(m *types.Message) (bool, error) {
	if not := m.Media == nil; not {
		return false, dispatcher.EndGroups
	}
	switch m.Media.(type) {
	case *tg.MessageMediaDocument:
		return true, nil
	case *tg.MessageMediaPhoto:
		return true, nil
	case tg.MessageMediaClass:
		return false, dispatcher.EndGroups
	default:
		return false, nil
	}
}

func extractXLinkText(msg *tg.Message) (string, string) {
	text := msg.Message
	for _, ent := range msg.Entities {
		if ue, ok := ent.(*tg.MessageEntityTextURL); ok {
			text = text + " " + ue.URL
		}
	}
	if strings.Contains(text, "x.com/") || strings.Contains(text, "twitter.com/") {
		m := xTweetRegex.FindStringSubmatch(text)
		if len(m) > 1 {
			url := fmt.Sprintf("https://x.com/i/status/%s", m[1])
			return url, m[1]
		}
	}
	return "", ""
}

func aria2Add(targetURL, title, downloadDir string, extra map[string]any) error {
	rpcURL := config.ValueOf.Aria2RPC
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		return fmt.Errorf("create download dir %s: %w", downloadDir, err)
	}

	opts := map[string]any{
		"dir": downloadDir,
		"out": title,
	}
	for k, v := range extra {
		opts[k] = v
	}

	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      "fsb-x",
		"method":  "aria2.addUri",
		"params": []any{
			[]string{targetURL},
			opts,
		},
	}

	body, _ := json.Marshal(payload)
	resp, err := http.Post(rpcURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("aria2 rpc: %w", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &result)
	if errMsg, ok := result["error"].(map[string]any); ok {
		return fmt.Errorf("aria2 error: %v", errMsg["message"])
	}
	return nil
}

func sendToAria2(mp4URL, title, downloadDir string) error {
	return aria2Add(mp4URL, title, downloadDir, nil)
}

// directLinkRegex matches bare http(s) URLs; extension filtering happens later.
var directLinkRegex = regexp.MustCompile(`(?i)https?://[^\s"'<>()\[\]]+`)

// directLinkExts are the extensions handed to aria2 without further inspection.
var directLinkExts = map[string]bool{
	".mp4": true, ".mkv": true, ".webm": true, ".mov": true, ".avi": true,
	".flv": true, ".ts": true, ".m4v": true, ".wmv": true,
	".mp3": true, ".m4a": true, ".aac": true, ".flac": true, ".wav": true, ".ogg": true,
	".zip": true, ".7z": true, ".rar": true, ".tar": true, ".gz": true, ".iso": true,
	".apk": true, ".pdf": true, ".epub": true,
}

// urlExt returns the lowercase extension of the URL path, ignoring the query.
func urlExt(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(path.Ext(u.Path))
}

// extractDirectURL returns the first URL that looks like a plain file download
// (known extension). HLS playlists are handled by extractM3u8Text instead.
func extractDirectURL(msg *tg.Message) string {
	text := msg.Message
	for _, ent := range msg.Entities {
		if ue, ok := ent.(*tg.MessageEntityTextURL); ok {
			text = text + " " + ue.URL
		}
	}
	for _, raw := range directLinkRegex.FindAllString(text, -1) {
		ext := urlExt(raw)
		if ext == ".m3u8" || !directLinkExts[ext] {
			continue
		}
		return strings.TrimRight(raw, ".,;")
	}
	return ""
}

// directLinkName builds a safe output name from the URL, keeping the extension.
func directLinkName(raw string) string {
	name := ""
	if u, err := url.Parse(raw); err == nil {
		base := path.Base(u.Path)
		if base != "" && base != "." && base != "/" {
			if dec, err := url.QueryUnescape(base); err == nil {
				base = dec
			}
			name = strings.TrimSpace(base)
		}
	}
	if name == "" {
		name = fmt.Sprintf("download-%d", time.Now().Unix())
	}
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}

func handleDirectLink(ctx *ext.Context, u *ext.Update, rawURL string) {
	name := directLinkName(rawURL)
	if !config.ValueOf.Aria2Enable {
		_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("link: %s", rawURL)), nil)
		return
	}
	err := aria2Add(rawURL, name, config.ValueOf.DownloadDir, map[string]any{
		"header":            []string{"User-Agent: Mozilla/5.0"},
		"check-certificate": "false",
	})
	if err != nil {
		utils.Logger.Sugar().Errorf("aria2 direct link failed: %v", err)
		_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("⚠️ aria2 failed: %v", err)), nil)
		return
	}
	utils.Logger.Sugar().Infof("aria2 direct link queued: %s", name)
	_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("✅ queued: %s", name)), nil)
}

func cleanFileName(s string, maxLen int) string {
	var result []rune
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '_' || r == '-' {
			result = append(result, r)
		}
	}
	cleaned := strings.TrimSpace(string(result))
	if len(cleaned) > maxLen {
		cleaned = cleaned[:maxLen]
	}
	return cleaned
}

func sendLink(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	peerChatId := ctx.PeerStorage.GetPeerById(chatId)
	if peerChatId.Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}
	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatId) {
		_, _ = ctx.Reply(u, ext.ReplyTextString("You are not allowed to use this bot."), nil)
		return dispatcher.EndGroups
	}

	// Handle m3u8 links
	if link := extractM3u8Text(u.EffectiveMessage.Message); link != "" {
		handleM3u8Link(ctx, u, link)
		return dispatcher.EndGroups
	}

	// Handle X/Twitter links
	xURL, tweetID := extractXLinkText(u.EffectiveMessage.Message)
	if xURL != "" {
		mp4, title, tweetText, err := ResolveXVideo(tweetID)
		if err != nil {
			utils.Logger.Sugar().Errorf("X resolve failed: %v", err)
			_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("X resolve failed: %v", err)), nil)
			return dispatcher.EndGroups
		}
		if mp4 == "" {
			_, _ = ctx.Reply(u, ext.ReplyTextString("No video found on this tweet."), nil)
			return dispatcher.EndGroups
		}

		// Hand the resolved X video to aria2
		fileName := cleanFileName(tweetText, 50)
		utils.Logger.Sugar().Infof("X filename debug: tweetText=%s, title=%s, fileName=%s", tweetText, title, fileName)
		if fileName == "" {
			fileName = title
		}
		fileName += ".mp4"
		if !config.ValueOf.Aria2Enable {
			_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("mp4: %s", mp4)), nil)
			return dispatcher.EndGroups
		}
		aria2Err := sendToAria2(mp4, fileName, filepath.Join(config.ValueOf.DownloadDir, "X"))
		if aria2Err != nil {
			utils.Logger.Sugar().Errorf("aria2 failed: %v", aria2Err)
			_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("⚠️ aria2 failed: %v", aria2Err)), nil)
		} else {
			_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("✅ X video queued: %s", fileName)), nil)
		}
		return dispatcher.EndGroups
	}

	// Handle plain file links (mp4/mkv/mp3/zip/...) — handed to aria2
	if u.EffectiveMessage.Media == nil {
		if link := extractDirectURL(u.EffectiveMessage.Message); link != "" {
			handleDirectLink(ctx, u, link)
			return dispatcher.EndGroups
		}
	}

	// Handle Telegram media
	supported, err := supportedMediaFilter(u.EffectiveMessage)
	if err != nil {
		return err
	}
	if !supported {
		_, _ = ctx.Reply(u, ext.ReplyTextString("Sorry, this message type is unsupported."), nil)
		return dispatcher.EndGroups
	}
	update, err := utils.ForwardMessages(ctx, chatId, config.ValueOf.LogChannelID, u.EffectiveMessage.ID)
	if err != nil {
		utils.Logger.Sugar().Error(err)
		_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("Error - %s", err.Error())), nil)
		return dispatcher.EndGroups
	}
	if len(update.Updates) < 2 {
		_, _ = ctx.Reply(u, ext.ReplyTextString("Error - unexpected update structure from Telegram"), nil)
		return dispatcher.EndGroups
	}
	msgIDUpdate, ok := update.Updates[0].(*tg.UpdateMessageID)
	if !ok {
		_, _ = ctx.Reply(u, ext.ReplyTextString("Error - unexpected update type"), nil)
		return dispatcher.EndGroups
	}
	messageID := msgIDUpdate.ID
	newMsg, ok := update.Updates[1].(*tg.UpdateNewChannelMessage)
	if !ok {
		_, _ = ctx.Reply(u, ext.ReplyTextString("Error - unexpected channel message update"), nil)
		return dispatcher.EndGroups
	}
	msg, ok := newMsg.Message.(*tg.Message)
	if !ok {
		_, _ = ctx.Reply(u, ext.ReplyTextString("Error - unexpected message type"), nil)
		return dispatcher.EndGroups
	}
	doc := msg.Media
	file, err := utils.FileFromMedia(doc)
	if err != nil {
		_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("Error - %s", err.Error())), nil)
		return dispatcher.EndGroups
	}
	fullHash := utils.PackFile(
		file.FileName,
		file.FileSize,
		file.MimeType,
		file.ID,
	)
	hash := utils.GetShortHash(fullHash)
	link := fmt.Sprintf("%s/stream/%d?hash=%s", config.ValueOf.Host, messageID, hash)

	// Auto-download Telegram video/audio through aria2
	if config.ValueOf.Aria2Enable && (strings.Contains(file.MimeType, "video") || strings.Contains(file.MimeType, "audio")) {
		tgFileName := file.FileName
		if tgFileName == "" {
			if strings.Contains(file.MimeType, "audio") {
				tgFileName = fmt.Sprintf("%d.mp3", u.EffectiveMessage.ID)
			} else {
				tgFileName = fmt.Sprintf("%d.mp4", u.EffectiveMessage.ID)
			}
		}
		aria2Err := sendToAria2(link, tgFileName, config.ValueOf.DownloadDir)
		if aria2Err != nil {
			utils.Logger.Sugar().Errorf("aria2 auto-download failed: %v", aria2Err)
		} else {
			utils.Logger.Sugar().Infof("aria2 auto-download started: %s", tgFileName)
			_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("✅ Video queued: %s", tgFileName)), nil)
		}
	}

	text := styling.Code(link)
	row := tg.KeyboardButtonRow{
		Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonURL{
				Text: "Download",
				URL:  link + "&d=true",
			},
		},
	}
	if strings.Contains(file.MimeType, "video") || strings.Contains(file.MimeType, "audio") || strings.Contains(file.MimeType, "pdf") {
		row.Buttons = append(row.Buttons, &tg.KeyboardButtonURL{
			Text: "Stream",
			URL:  link,
		})
	}
	markup := &tg.ReplyInlineMarkup{
		Rows: []tg.KeyboardButtonRow{row},
	}
	if strings.Contains(link, "http://localhost") {
		_, err = ctx.Reply(u, ext.ReplyTextStyledText(text), &ext.ReplyOpts{
			NoWebpage:        false,
			ReplyToMessageId: u.EffectiveMessage.ID,
		})
	} else {
		_, err = ctx.Reply(u, ext.ReplyTextStyledText(text), &ext.ReplyOpts{
			Markup:           markup,
			NoWebpage:        false,
			ReplyToMessageId: u.EffectiveMessage.ID,
		})
	}
	if err != nil {
		utils.Logger.Sugar().Error(err)
		_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("Error - %s", err.Error())), nil)
	}
	return dispatcher.EndGroups
}

// ResolveXVideo resolves X/Twitter video via fxtwitter API with Cloudflare DoH.
// Returns: videoURL, screenName, tweetText, error
func ResolveXVideo(tweetID string) (string, string, string, error) {
	ip, err := resolveDoh("https://1.1.1.1/dns-query", "api.fxtwitter.com")
	if err != nil {
		ip, err = resolveDoh("https://8.8.8.8/resolve", "api.fxtwitter.com")
		if err != nil {
			return "", "", "", fmt.Errorf("dns: %w", err)
		}
	}

	transport := &http.Transport{
		DialTLS: func(network, addr string) (net.Conn, error) {
			_, port, _ := net.SplitHostPort(addr)
			if port == "" {
				port = "443"
			}
			return tls.DialWithDialer(
				&net.Dialer{Timeout: 10 * time.Second},
				network,
				net.JoinHostPort(ip, port),
				&tls.Config{ServerName: "api.fxtwitter.com"},
			)
		},
	}
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}

	req, _ := http.NewRequest("GET", fmt.Sprintf("https://api.fxtwitter.com/2/status/%s", tweetID), nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var obj map[string]any
	_ = json.Unmarshal(b, &obj)
	if code, ok := obj["code"].(float64); ok && int(code) == 404 {
		return "", "", "", fmt.Errorf("api 404")
	}

	tweet, _ := obj["status"].(map[string]any)
	if tweet == nil {
		return "", "", "", fmt.Errorf("no tweet")
	}
	media, _ := tweet["media"].(map[string]any)
	if media == nil {
		return "", "", "", fmt.Errorf("no media")
	}
	medias, _ := media["all"].([]any)
	if medias == nil {
		return "", "", "", fmt.Errorf("no media items")
	}

	var videoURL string
	author, _ := tweet["author"].(map[string]any)
	screenName := tweetID
	if author != nil {
		if sn, ok := author["screen_name"].(string); ok && sn != "" {
			screenName = sn
		}
	}
	tweetText, _ := tweet["text"].(string)

	for _, m := range medias {
		mm, _ := m.(map[string]any)
		if mm["type"] != "video" {
			continue
		}
		mainURL, _ := mm["url"].(string)
		if mainURL != "" {
			videoURL = mainURL
			break
		}
	}

	if videoURL != "" {
		return videoURL, screenName, tweetText, nil
	}
	return "", "", "", fmt.Errorf("no mp4")
}

func resolveDoh(endpoint, name string) (string, error) {
	url := fmt.Sprintf("%s?name=%s&type=A", endpoint, name)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/dns-json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var obj map[string]any
	body, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(body, &obj)
	ans, _ := obj["Answer"].([]any)
	for _, a := range ans {
		rec, _ := a.(map[string]any)
		if rec["type"] == 1 || rec["type"] == float64(1) {
			return rec["data"].(string), nil
		}
	}
	return "", fmt.Errorf("no A")
}
