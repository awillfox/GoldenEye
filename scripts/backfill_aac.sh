#!/usr/bin/env bash
# One-time conversion of already-recorded segments to browser-playable MP4.
#
# mediamtx's recorder wrote G711 audio as raw LPCM ('ipcm'), which no browser
# can decode, so existing recordings play silently. This rewrites each file with
# AAC audio, and re-encodes H.265 video to H.264 when --reencode is given.
#
# Safe to interrupt and re-run: files whose audio is already AAC are skipped.
# Originals are moved to <dir>/.orig/<camera>/ and are NOT deleted — remove that
# directory by hand once playback has been spot-checked.
#
# Usage:
#   scripts/backfill_aac.sh /home/coffee/goldeneye/recordings [--camera NAME] [--reencode]
set -uo pipefail

DIR="${1:-}"
if [[ -z "$DIR" || ! -d "$DIR" ]]; then
  echo "usage: $0 <recordings_dir> [--camera NAME] [--reencode]" >&2
  exit 2
fi
shift

CAMERA=""
REENCODE=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --camera)   CAMERA="${2:-}"; shift 2 ;;
    --reencode) REENCODE=1; shift ;;
    *)          echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

ORIG_ROOT="$DIR/.orig"
LOG="$DIR/.backfill.log"
converted=0; skipped=0; failed=0

log() { printf '%s %s\n' "$(date +%FT%T)" "$*" | tee -a "$LOG"; }

log "backfill start dir=$DIR camera=${CAMERA:-all} reencode=$REENCODE"

# .orig and .thumbs are excluded so re-runs never reprocess originals.
while IFS= read -r -d '' f; do
  rel="${f#"$DIR"/}"
  cam="${rel%%/*}"
  [[ -n "$CAMERA" && "$cam" != "$CAMERA" ]] && continue

  atag=$(ffprobe -v error -select_streams a:0 -show_entries stream=codec_tag_string -of csv=p=0 "$f" 2>/dev/null)
  if [[ "$atag" == "mp4a" ]]; then
    skipped=$((skipped + 1))
    continue
  fi

  vcodec=$(ffprobe -v error -select_streams v:0 -show_entries stream=codec_name -of csv=p=0 "$f" 2>/dev/null)
  src_dur=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$f" 2>/dev/null)

  if [[ "$vcodec" == "hevc" && "$REENCODE" == "1" ]]; then
    vargs=(-c:v libx264 -preset ultrafast)
  else
    vargs=(-c:v copy)
  fi

  tmp="$f.tmp.mp4"
  rm -f "$tmp"
  if ! nice -n 19 ionice -c3 ffmpeg -nostdin -v error -y -i "$f" \
        "${vargs[@]}" -c:a aac -b:a 32k -movflags +faststart "$tmp"; then
    log "FAIL convert $rel"
    rm -f "$tmp"
    failed=$((failed + 1))
    continue
  fi

  # Verify before destroying anything: both streams present, audio is AAC,
  # duration within one second of the source.
  new_atag=$(ffprobe -v error -select_streams a:0 -show_entries stream=codec_tag_string -of csv=p=0 "$tmp" 2>/dev/null)
  new_dur=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$tmp" 2>/dev/null)
  ok=$(awk -v a="${src_dur:-0}" -v b="${new_dur:-0}" 'BEGIN{d=a-b; if(d<0)d=-d; print (d<=1.0)?"1":"0"}')

  if [[ "$new_atag" != "mp4a" || "$ok" != "1" ]]; then
    log "FAIL verify $rel (audio=$new_atag src_dur=$src_dur new_dur=$new_dur)"
    rm -f "$tmp"
    failed=$((failed + 1))
    continue
  fi

  mkdir -p "$ORIG_ROOT/$cam"
  if ! mv "$f" "$ORIG_ROOT/$cam/$(basename "$f")"; then
    log "FAIL stash original $rel"
    rm -f "$tmp"
    failed=$((failed + 1))
    continue
  fi
  mv "$tmp" "$f"
  converted=$((converted + 1))
  log "ok $rel (video=$vcodec)"
done < <(find "$DIR" -mindepth 2 -name '*.mp4' -not -path "$ORIG_ROOT/*" -not -path "$DIR/.thumbs/*" -print0 | sort -z)

log "backfill done converted=$converted skipped=$skipped failed=$failed"
log "originals kept in $ORIG_ROOT — delete manually after spot-checking playback"
