#!/bin/sh
# Optional log watcher: hands every stream link the bot prints to aria2.
#
# Only useful when the build you run does NOT hand off to aria2 itself (see
# ARIA2_ENABLE in fsb.env). Kept here because the locking pattern is the
# interesting part: a watcher started from rc.local twice will otherwise run
# twice, race on the same log and enqueue the same URL n times.

export TZ=CST-8

STREAM_LOG="${STREAM_LOG:-/tmp/stream_urls.log}"
SEEN_FILE="${SEEN_FILE:-/mnt/sda1/fsb/seen_urls.txt}"
ARIA2_RPC="${ARIA2_RPC:-http://127.0.0.1:6800/jsonrpc}"
DOWNLOAD_DIR="${DOWNLOAD_DIR:-/mnt/sda1/fsb/downloads}"
LOCK_FILE="${LOCK_FILE:-/tmp/stream_watch.pid}"

log() { echo "[$(date '+%H:%M:%S')] $*"; }

# ---- single instance -------------------------------------------------------
# kill -0 proves the pid exists; reading its cmdline guards against PID reuse
# after a reboot. The lock lives in tmpfs, so a clean boot always starts fresh.
if [ -f "$LOCK_FILE" ]; then
	holder=$(cat "$LOCK_FILE" 2>/dev/null)
	if [ -n "$holder" ] && kill -0 "$holder" 2>/dev/null &&
		grep -q "stream-watch" "/proc/$holder/cmdline" 2>/dev/null; then
		log "already running (pid $holder), exiting"
		exit 0
	fi
	log "stale lock (pid ${holder:-none}), taking over"
	rm -f "$LOCK_FILE"
fi
echo $$ > "$LOCK_FILE"
cleanup() {
	# only the owner may delete the lock: command substitution and pipelines
	# run in subshells whose exit would otherwise drop the parent's lock
	[ "$(cat "$LOCK_FILE" 2>/dev/null)" = "$$" ] && rm -f "$LOCK_FILE"
}
trap cleanup EXIT INT TERM

touch "$SEEN_FILE" "$STREAM_LOG"

aria2_add() {
	url="$1"; name="$2"
	body="{\"jsonrpc\":\"2.0\",\"id\":\"watch\",\"method\":\"aria2.addUri\",\"params\":[[\"${url}\"],{\"dir\":\"${DOWNLOAD_DIR}\",\"out\":\"${name}\",\"check-certificate\":\"false\",\"header\":[\"User-Agent: Mozilla/5.0\"]}]}"
	result=$(curl -s -H "Content-Type: application/json" -d "$body" "$ARIA2_RPC")
	log "aria2: $result"
}

process_log() {
	prev_url=""
	prev_fname=""
	while IFS= read -r line; do
		url=$(echo "$line" | grep -oE "http://[0-9.:]+/stream/[0-9]+\?hash=[a-zA-Z0-9]+")
		if [ -n "$url" ]; then
			if [ -n "$prev_url" ] && ! grep -qF "$prev_url" "$SEEN_FILE" 2>/dev/null; then
				echo "$prev_url" >> "$SEEN_FILE"
				[ -z "$prev_fname" ] && prev_fname="fsb_$(echo "$prev_url" | grep -oE 'stream/[0-9]+' | grep -oE '[0-9]+').mp4"
				log "new $prev_fname -> $DOWNLOAD_DIR"
				aria2_add "$prev_url" "$prev_fname"
			fi
			prev_url="$url"
			prev_fname=""
		fi
	done < "$STREAM_LOG"

	if [ -n "$prev_url" ] && ! grep -qF "$prev_url" "$SEEN_FILE" 2>/dev/null; then
		sleep 2
		echo "$prev_url" >> "$SEEN_FILE"
		[ -z "$prev_fname" ] && prev_fname="fsb_$(echo "$prev_url" | grep -oE 'stream/[0-9]+' | grep -oE '[0-9]+').mp4"
		log "new $prev_fname -> $DOWNLOAD_DIR"
		aria2_add "$prev_url" "$prev_fname"
	fi
}

log "watching $STREAM_LOG -> aria2"
process_log
inotifywait -m -e modify "$STREAM_LOG" | while read -r _; do
	sleep 1
	process_log
done
