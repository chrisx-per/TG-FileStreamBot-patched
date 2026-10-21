# Running fsb on an OpenWrt router

This is the deployment this fork is developed against: a small aarch64 router,
USB storage, no ffmpeg, no Docker, no CUDA — just a static Go binary, aria2, and
whatever the router already ships (BusyBox).

Measured on that class of hardware: **~31 MB RSS** for the bot, a few MB for
aria2, on a **490 MB RAM / 1.8 TB USB** box. The bot's own job is small — it
multiplexes Telegram chunks into an HTTP stream and decrypts HLS segments.
Anything that is a plain bulk transfer is pushed to aria2 over JSON-RPC and
never touches the bot's heap.

```
Telegram ──MTProto──> fsb ──HTTP :8080──> VLC / browser / curl
                        │
                        └──JSON-RPC──> aria2 ──> USB disk
```

## Why the pieces are split this way

- **aria2 has no HLS support.** Give it an `.m3u8` and you get the playlist file,
  not the video. So the HLS work (variant selection, AES-128, `EXT-X-MAP`)
  happens in-process; that is the only reason `m3u8.go` exists.
- **The bot must never block on a transfer.** Resolved X videos, plain file
  links and Telegram video/audio are queued to aria2 and the handler returns
  immediately.
- **One supervisor, not `rc.local` + `&`.** `rc.local` runs once and has no
  restart policy; re-running a hand-written start script is how you end up with
  several copies of the same watcher racing on the same log.

## Prerequisites

```sh
opkg update
opkg install aria2 curl inotifywait kmod-usb-storage-uas kmod-fs-ext4
# optional, only if your traffic needs a userspace proxy
opkg install proxychains-ng
```

Use a real filesystem on the USB disk (`ext4`), not `vfat`/`ntfs-3g`: HLS
downloads are written sequentially and many small files on a FUSE filesystem
will crawl.

## Layout

```
/mnt/sda1/fsb/
├── fsb                # static binary from the release page
├── fsb.env            # secrets + runtime config (never commit this)
├── downloads/         # DOWNLOAD_DIR; downloads/X for X/Twitter videos
├── fsb.log
└── stream-watch.sh    # optional, see below
```

## 1. Install the binary

Pick the asset that matches `uname -m`:

```sh
uname -m                       # aarch64 → fsb-linux-arm64, x86_64 → fsb-linux-amd64
mkdir -p /mnt/sda1/fsb && cd /mnt/sda1/fsb
wget -O fsb https://github.com/chrisx-per/TG-FileStreamBot-patched/releases/latest/download/fsb-linux-arm64
chmod +x fsb
```

`SHA256SUMS` is published next to the binaries if you want to verify first.

## 2. Configure

```sh
cp deploy/fsb.env.example /mnt/sda1/fsb/fsb.env
vi /mnt/sda1/fsb/fsb.env
```

Minimum: `API_ID`, `API_HASH`, `BOT_TOKEN`, `LOG_CHANNEL`, `HOST`, `PORT`,
`DOWNLOAD_DIR`. `HOST` is what ends up inside the links the bot sends, so a LAN
IP is fine for a LAN-only setup.

The bot reads `./fsb.env` **relative to its working directory** — that is why
the service below sets `cwd`.

## 3. aria2 with RPC enabled

```sh
uci set aria2.main.rpc_listen_port='6800'
uci set aria2.main.rpc_secret=''          # keep it on loopback if unset
uci commit aria2
/etc/init.d/aria2 enable && /etc/init.d/aria2 restart
```

Keep the RPC listener on `127.0.0.1` — fsb talks to it from the same box. If you
prefer aria2 on another machine, set `ARIA2_RPC` accordingly and make sure its
`dir` option points at storage that machine can write.

## 4. Run it under procd

```sh
cp deploy/fsb.init /etc/init.d/fsb
chmod +x /etc/init.d/fsb
vi /etc/init.d/fsb          # set WORKDIR if you do not use /mnt/sda1/fsb
/etc/init.d/fsb enable
/etc/init.d/fsb start
logread -f -e fsb
```

This replaces the `rc.local` + `start.sh` + `&` pattern: procd owns exactly one
instance, sets the working directory, and respawns on crash with a backoff.

## 5. Verify

```sh
pidof fsb                                   # one pid, not several
netstat -ltnp | grep :8080                  # LISTEN
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/     # 200
tail -20 /mnt/sda1/fsb/fsb.log              # "Client started", "Server started"
```

Then in Telegram: send an `.m3u8` link (expect `⏬ m3u8 download started`), and a
plain `.mp4` link with no attachment (expect `✅ queued: ....mp4`).

## 6. Optional: legacy log watcher

Only needed if you run a build without the aria2 hand-off and want stream links
enqueued anyway. `deploy/stream-watch.sh` tails the bot's log and adds matching
URLs to aria2. It carries a PID-file lock with a cmdline check, because this is
exactly the kind of script that ends up running four times after a few manual
restarts.

```sh
cp deploy/stream-watch.sh /mnt/sda1/fsb/
sh /mnt/sda1/fsb/stream-watch.sh >> /mnt/sda1/fsb/stream-watch.log 2>&1 &
```

## Resource notes

- `STREAM_BUFFER_COUNT` × chunk size is the streaming memory floor. On a
  490 MB box, `4` concurrency and `8` buffered chunks is comfortable; raising
  both is how you meet the OOM killer.
- Set `oom_score_adj` negative for the bot and aria2 so the kernel sacrifices
  caches first:
  `echo -500 > /proc/$(pidof fsb)/oom_score_adj`
- If you have no swap, add a file swap on the USB disk and `swapon` it before
  starting the bot; a burst of concurrent streams is otherwise fatal.

## Gotchas that cost real time

| Symptom | Cause |
| --- | --- |
| Two or more copies of the bot/watcher after a while | Something re-runs your start script. `rc.local` runs once; `procd` supervises. Never launch a watcher without a lock. |
| `pgrep -x fsb` returns nothing while `pidof fsb` works | BusyBox `pgrep -x` (1.36.x) does not match `comm` reliably. Use `pidof`. |
| `pkill -f <script>.sh` kills your own SSH session | The pattern matches the remote command line of the session invoking it. Use `pkill -f "<scr[i]pt>.sh"` (bracket trick) or run the cleanup from a script file. |
| `proxychains4 ./fsb` has no effect | The release binaries are static (`CGO_ENABLED=0`), so `LD_PRELOAD` is ignored. Route at the network layer (policy routing / transparent proxy) instead. |
| HLS downloads stall or fail on the last segment | Live playlists keep growing. Static/VOD playlists work; for live streams prefer letting aria2 or a recorder handle it. |
| `DOWNLOAD_DIR` missing after an upgrade | Older builds hardcoded the output path. Set `DOWNLOAD_DIR` explicitly in `fsb.env`. |
