<h1 align="center">Telegram File Stream Bot</h1>
<p align="center">
  </a>
  <p align="center">
    <a herf="https://github.com/EverythingSuckz/TG-FileStreamBot">
        <img src="https://filestream.bot/logo.svg" height="100" width="100" alt="File Stream Bot Logo">
    </a>
</p>
  <p align="center">
    A Telegram bot to <b>generate direct link</b> for your Telegram files.
    <br />
    << <a href="https://filestream.bot/getting-started">Visit docs</a> |  <a href="https://filestream.bot/support">Support</a> >>
  </p>
</p>




## Patches in this fork

Personal fork of [EverythingSuckz/TG-FileStreamBot](https://github.com/EverythingSuckz/TG-FileStreamBot) (golang branch).
Only the changes listed below are added on top of upstream; everything else is untouched.

| Feature | Where | Notes |
| --- | --- | --- |
| X / Twitter video resolution | `internal/commands/stream.go` (`ResolveXVideo`) | Resolves `x.com` / `twitter.com` status links through the public fxtwitter API. DNS is resolved over DoH (`1.1.1.1`, fallback `8.8.8.8`) and the request is pinned to that IP with the proper SNI, so it works behind polluted DNS. |
| HLS / m3u8 download | `internal/commands/m3u8.go` | A message containing an `.m3u8` URL (plain text or text-url entity) is downloaded sequentially: master-playlist variant selection, AES-128 decryption, `EXT-X-MAP` init segment, `EXT-X-BYTERANGE` rejected. Saved to `DOWNLOAD_DIR`. |
| aria2 hand-off | `internal/commands/stream.go` (`sendToAria2`, `handleDirectLink`) | Resolved X videos and Telegram video/audio are queued over aria2 JSON-RPC so the bot does not block on the transfer. Disable with `ARIA2_ENABLE=false`. |
| Plain file links | `internal/commands/stream.go` (`extractDirectURL`) | A message with no attachment that contains a link ending in a known extension (`.mp4`, `.mkv`, `.mp3`, `.zip`, `.iso`, …) is queued to aria2 directly, with the URL basename as output name. |
| Config / CLI | `config/config.go` | New options: `DOWNLOAD_DIR`, `ARIA2_ENABLE`, `ARIA2_RPC` (also `--download-dir`, `--aria2-enable`, `--aria2-rpc`). |

Extra environment variables (all optional):

```env
DOWNLOAD_DIR=./downloads                     # HLS output + aria2 target, X videos go to $DOWNLOAD_DIR/X
ARIA2_ENABLE=true                            # set false to skip aria2 entirely
ARIA2_RPC=http://127.0.0.1:6800/jsonrpc      # aria2 JSON-RPC endpoint
```

Build and test:

```bash
go build ./cmd/fsb
go test ./...
# the live HLS test is opt-in:
M3U8_LIVE_TEST=1 go test ./internal/commands -run TestDownloadM3u8Live -v
```

### Notes on the above

- No bot token, API hash, host name or path from any real deployment is stored in this repository.
  Runtime configuration comes from `fsb.env` (git-ignored) or the environment.
- `vendor/` is git-ignored; use `go mod download` / `go mod vendor` as needed.

### Message routing order

1. `.m3u8` link → built-in HLS downloader (`DOWNLOAD_DIR`)
2. `x.com` / `twitter.com` status link → fxtwitter resolve, then aria2
3. plain file link (no attachment, known extension) → aria2
4. Telegram media → stream link is returned, plus aria2 auto-download for video/audio

Note that aria2 itself has no HLS support: handing it a `.m3u8` only fetches the
playlist file, which is why step 1 exists in-process.

## Credits

- [@celestix](https://github.com/celestix) for [gotgproto](https://github.com/celestix/gotgproto)
- [@divyam234](https://github.com/divyam234/teldrive) for his [Teldrive](https://github.com/divyam234/teldrive) Project
- [@krau](https://github.com/krau) for adding image support

## Copyright

Copyright (C) 2026 [EverythingSuckz](https://github.com/EverythingSuckz) under [GNU Affero General Public License](https://www.gnu.org/licenses/agpl-3.0.en.html).

TG-FileStreamBot is Free Software: You can use, study share and improve it at your
will. Specifically you can redistribute and/or modify it under the terms of the
[GNU Affero General Public License](https://www.gnu.org/licenses/agpl-3.0.en.html) as
published by the Free Software Foundation, either version 3 of the License, or
(at your option) any later version. Also keep in mind that all the forks of this repository MUST BE OPEN-SOURCE and MUST BE UNDER THE SAME LICENSE.
