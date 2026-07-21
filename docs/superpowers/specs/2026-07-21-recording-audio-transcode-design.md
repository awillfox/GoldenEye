# Browser-playable recordings: AAC audio, clock-aligned segments

Date: 2026-07-21
Status: approved, pending implementation plan

## Problem

Recordings play silently in every browser, and the H.265 camera shows no picture at all in Firefox.

Evidence gathered on the deployed host (`coffee@100.86.43.70`, `/home/coffee/goldeneye`):

```
$ ffprobe .../front-door/2026-07-21_12-20-30-873772.mp4
index=0 codec_name=h264      codec_tag_string=avc1   video
index=1 codec_name=pcm_s16be codec_tag_string=ipcm   audio 8000 Hz mono

$ ffmpeg -af volumedetect ...
mean_volume: -23.8 dB   max_volume: -0.1 dB      # audio is real, not silence
```

Chain:

1. Cameras publish G711 audio (`/v3/paths/list`: `front-door ['H264','G711']`, `cashier ['H265','G711']`).
2. mediamtx v1.9.3's fMP4 recorder writes G711 as raw LPCM in an `ipcm` box (ISO/IEC 23003-5).
3. `internal/web/server.go:212` serves that file directly with `http.ServeFile`. Browsers decode only AAC, MP3, Opus, FLAC and Vorbis inside MP4 — the `ipcm` track is dropped, so playback is silent.

`cashier` records 3840x2160 HEVC, which Firefox on Linux cannot decode either, so that camera's recordings show nothing at all.

This predates the WebRTC preview work (`d7a2fe2`): that commit left the recording path untouched and the `mediamtx` binary on the host is unchanged (dated 2024-10-20). Retention had already deleted every file older than 2026-07-20, so no pre-change file survives for direct comparison — the claim rests on the code and binary being unchanged, not on an A/B of old files.

A second requirement surfaced during design: segment boundaries must align to the wall clock. A stream starting at 10:47 with `segment_minutes: 10` must cut at 11:00, then 11:10, 11:20, and so on. mediamtx's recorder cannot do this; it only supports `recordSegmentDuration` measured from stream start.

## Goals

- Recordings play with sound in Firefox and Chrome.
- `cashier` recordings play with picture in Firefox.
- Segment boundaries align to wall-clock multiples of `segment_minutes`.
- Existing ~34 GB of recorded footage is converted in place, one time.

## Non-goals

- Changing the WebRTC live preview. It must keep G711, because WebRTC carries no AAC.
- Changing camera-side settings.
- Re-encoding for storage savings.

## Measurements (host, 30-second live samples)

| Job | CPU |
|---|---|
| `front-door` H264 copy + G711 to AAC | 3% of one core |
| `cashier` 4K HEVC decode only | 49% |
| `cashier` 4K HEVC to H264 `ultrafast` | 140% (1.4 of 4 cores, sustained) |
| `cashier` scaled to 1080p + H264 | 96% |

Host: Intel i5-3570, 4 cores, 8 GB RAM, 815 GB free, `ffmpeg` 8.0.1 with `libx264` and `aac`.

Accepted cost: 140% for `cashier` at full 4K, plus ~3% for each of the two H264 cameras. Roughly 1.5 of 4 cores in steady state.

## Design

### Recording pipeline

mediamtx stops recording. GoldenEye supervises one `ffmpeg` per enabled camera, and that process writes segments straight to disk.

| Path | Role | mediamtx recorder |
|---|---|---|
| `<name>` | native RTSP source, always connected, feeds everything | off (`record: false`) |
| `<name>-preview` | unchanged: on-demand H264 + G711 transcode for the WebRTC preview | off |

The `<name>-preview` path and its `runOnDemand` config are untouched by this work.

Command per camera:

```
ffmpeg -nostdin -loglevel warning -rtsp_transport tcp \
  -i rtsp://127.0.0.1:<RTSP_PORT>/<name> \
  <video args> \
  -c:a aac -b:a 32k \
  -f segment -segment_time <segment_minutes*60> -segment_atclocktime 1 \
  -segment_format mp4 \
  -segment_format_options movflags=+frag_keyframe+empty_moov+default_base_moof \
  -reset_timestamps 1 -strftime 1 \
  <RECORDINGS_DIR>/<name>/%Y-%m-%d_%H-%M-%S.mp4
```

`<video args>` is `-c:v copy` normally, and for cameras flagged `transcode_video`:
`-c:v libx264 -preset ultrafast -tune zerolatency -pix_fmt yuv420p -g 60`.

Input is mediamtx's already-connected native path, so the camera is never opened twice.

Verified on the host before adopting this design:

```
started 12:45:14 -> 2026-07-21_12-45-14.mp4  (partial, 47 s)
                    2026-07-21_12-46-01.mp4  60.08 s
                    2026-07-21_12-47-01.mp4  60.08 s   avc1 + mp4a
                    2026-07-21_12-48-01.mp4  (process killed mid-write, still readable)
```

Two properties confirmed by that run: `-segment_atclocktime 1` cuts on clock boundaries, and the fragmented-MP4 flags leave a killed segment playable. Cuts land about one second past the boundary because `-c:v copy` must wait for the camera's next keyframe. That lag is accepted.

### Downstream components

Unchanged: the indexer (`parseSegmentTime` at `internal/index/indexer.go:225` already parses `2026-07-21_12-45-14.mp4`), thumbnails, the rotation sweeper, and the VOD handler.

One addition: GoldenEye must `MkdirAll` each camera's directory before starting `ffmpeg`, because the segment muxer will not create it.

### Supervisor

New package `internal/recorder`.

```go
type Supervisor struct { ... }

func New(env *config.Env, settings *config.Settings) *Supervisor
func (s *Supervisor) Sync(ctx context.Context, cams []sqlc.Camera)  // reconcile to desired set
func (s *Supervisor) Stop()                                          // graceful, on shutdown
func (s *Supervisor) Status() map[string]ProcState                   // for the dashboard
```

- One goroutine per enabled camera: build command, start, wait, restart on exit with backoff from 1 s to a 30 s cap. Exit is expected and normal — cameras drop.
- Identity is the built command string. `Sync` restarts a camera only when its command changes (rename, RTSP URL, `transcode_video`, `segment_minutes`); otherwise the running process is left alone.
- Disabled or deleted cameras: SIGTERM, wait 5 s, then SIGKILL.
- `ffmpeg` stderr is forwarded to `slog` at warn level, tagged with the camera name.
- `buildArgs(cam, settings, env) []string` is a pure function so it can be table-tested without spawning anything.

Wiring:

- `main.go` constructs the supervisor, calls `Sync` after the initial mediamtx reconcile, and calls `Stop` during graceful shutdown.
- `camera.Service` gains a small interface (`RecorderSyncer` with one `Sync` method) called after `Create`, `Update`, and `Delete`, so CRUD takes effect immediately. An interface, not a concrete type, keeps `recorder` free of an import cycle.

### `transcode_video` flag

Reconcile runs at startup, when a stream may not be connected yet, so probing live tracks to detect H.265 is racy. The decision is stored instead.

- `schema.hcl`: `cameras.transcode_video boolean not null default false`.
- Workflow per CLAUDE.md: edit `schema.hcl`, `task migrate-dev`, `task generate-sql-schema`, `task sqlcgen`.
- `internal/sql/cameras.sql`: `CreateCamera` and `UpdateCamera` carry the new column.
- Camera form in `dashboard.html` gains a checkbox: "Transcode video to H.264 (needed for H.265 cameras)". `editCamera(...)` and the create/update handlers in `internal/web/server.go` pass it through.
- On deploy, `cashier` gets it checked; the two H264 cameras stay copy-only.

The existing nullable `cameras.codec` column is left alone. It is descriptive, not load-bearing.

### Stale-recording detection

Because mediamtx no longer records, a wedged supervisor means silent data loss. Surface it.

- New sqlc query returning the newest `started_at` per camera from `recordings`.
- The dashboard marks a camera stale when `now() - max(started_at) > 2 * segment_minutes`, and the same condition logs a warning on each indexer scan.
- DB-backed rather than in-memory, so the signal survives a GoldenEye restart.

### Backfill of existing footage

One-time `scripts/backfill_aac.sh`, run manually on the host under `tmux`, over the ~34 GB already recorded.

Per file:

1. `ffprobe` the audio tag. Skip if already `mp4a` — this makes the script resumable.
2. Convert to `<file>.tmp.mp4`: `-c:a aac -b:a 32k`, with `-c:v copy` for H264 files and `-c:v libx264 -preset ultrafast` for `cashier`'s HEVC files.
3. Verify the output: both streams present, audio tag `mp4a`, duration within 1 s of the source. On failure, delete the temp file, log, and move on without touching the original.
4. Move the original to `RECORDINGS_DIR/.orig/<camera>/<file>`, then rename the temp file into the original path. The filename never changes, so `recordings` rows, thumbnails, and `duration_secs` stay valid.

Run with `nice -n 19` and `ionice -c3` so live recording keeps priority. Full 4K re-encode of `cashier` is in scope and runs at roughly 1:1 with footage duration — about 36 hours of wall clock for the current backlog, to be run overnight and across following days.

`.orig` is deleted manually after spot-checking playback. The rotation sweeper does not know about `.orig`, so leaving it in place would grow unmanaged; deletion is part of the operator checklist below.

Disk: originals plus converted copies peak near 68 GB against 815 GB free.

## Testing

Unit:

- `recorder.buildArgs`: table test over copy vs. transcode, `segment_minutes` values, and path construction.
- `recorder.Supervisor`: restart-on-exit and `Stop` behaviour, driven by an injected command builder that runs `false` and `sleep` instead of `ffmpeg`.
- `Sync`: no restart when the command is unchanged; restart when it changes; stop when a camera is disabled.

Manual verification on the host after deploy (evidence to capture, not assume):

1. `ffprobe` a new segment: expect `avc1` + `mp4a`.
2. `ls` a camera directory: expect boundaries at `:00`, `:10`, `:20` (+~1 s).
3. Play a new recording in Firefox: expect sound; for `cashier`, expect picture.
4. `pidstat` on the `ffmpeg` processes: expect roughly 140% for `cashier`, ~3% each for the others.
5. Confirm the stale badge does not fire while recording is healthy.

## Blast radius and rollback

Worst case: recording stops entirely and the loss is silent, because mediamtx's recorder is no longer a fallback. Mitigations: stale-recording detection, restart with backoff, and a check for new segments within the first ten minutes after deploy.

Rollback: redeploy the previous binary, which reconciles `record: true` back onto the native paths on startup. Segments already written by either pipeline remain playable and indexed. The one irreversible step is the backfill's overwrite of original files, which is why `.orig` is retained until spot-checked.

## Operator checklist (deploy order)

1. `task build`, rsync the binary.
2. Apply the schema migration.
3. Restart GoldenEye, confirm three `ffmpeg` processes and new segments within one segment interval.
4. Check `transcode_video` on `cashier` in the dashboard.
5. Verify playback in Firefox for one recording per camera.
6. Start the backfill in `tmux`.
7. Spot-check converted files, then delete `RECORDINGS_DIR/.orig`.

## Assumptions

- Cameras keep emitting G711; if one is switched to AAC, `-c:a aac` re-encodes needlessly but still works.
- The i5-3570 absorbs ~1.5 cores of steady encoding alongside PostgreSQL, mediamtx, and GoldenEye. Measured per-job, not yet measured all-at-once — verify in step 3 above.
- `segment_minutes` divides 60 evenly. `-segment_atclocktime` aligns to multiples of `segment_time` within each hour, so a value like 7 would produce uneven cuts at the hour boundary. Current value is 10.
- Retention stays at 30 days; storage math is unchanged because segment content is unchanged in size for copy cameras. `cashier`'s H264 re-encode may grow its footprint relative to HEVC.
