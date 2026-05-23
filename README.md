# GoldenEye

Self-hosted NVR. Connects to IP cameras over RTSP, records continuously to disk, and serves a dashboard for live viewing and playback behind a login.

## How It Works

```
IP cameras ──RTSP──► mediamtx ──► fMP4 segments on disk
                         │
                    HLS (on demand)
                         │
browser ◄── auth ── goldeneye ──► PostgreSQL
```

- **mediamtx** pulls each camera once and serves both the recorder and HLS from one connection
- **goldeneye** is the Go control plane: camera management, auth, rotation, dashboard

## Prerequisites

- Go 1.22+
- PostgreSQL (running, accessible)
- `task` CLI — `go install github.com/go-task/task/v3/cmd/task@latest`
- `atlas` CLI — https://atlasgo.io/docs
- `sqlc` CLI — https://sqlc.dev

mediamtx and Tailwind CLI are downloaded automatically by `task dev` / `task css`.

## Quick Start

```bash
# 1. Copy and fill config
cp .env.example .env
# edit .env: set PSQL_URL, MASTER_KEY, ADMIN_USER, ADMIN_PASS

# 2. Generate MASTER_KEY
openssl rand -base64 32

# 3. Apply database schema
task migrate-dev

# 4. Build CSS + download hls.js
task css

# 5. Start (two terminals)
task mediamtx   # terminal 1
task run        # terminal 2
```

Open http://localhost:8080 and log in with your `ADMIN_USER` / `ADMIN_PASS`.

## Configuration

### `.env` — infrastructure/secrets (set once at deploy time)

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | HTTP listen port |
| `PSQL_URL` | — | PostgreSQL DSN (required) |
| `MASTER_KEY` | — | JWT signing secret, ≥ 32 bytes (required) |
| `ADMIN_USER` | — | Bootstrap admin username (first run only) |
| `ADMIN_PASS` | — | Bootstrap admin password (first run only) |
| `RECORDINGS_DIR` | `./recordings` | Where fMP4 segments are stored |
| `MEDIAMTX_API` | `http://127.0.0.1:9997` | mediamtx control API |
| `MEDIAMTX_HLS` | `http://127.0.0.1:8888` | mediamtx HLS endpoint |
| `MEDIAMTX_RTSP` | `rtsp://127.0.0.1:8554` | mediamtx RTSP endpoint |

### `config.json` — runtime settings (editable from dashboard)

| Key | Default | Description |
|-----|---------|-------------|
| `retention_days` | `30` | Delete recordings older than N days |
| `segment_minutes` | `10` | Length of each recorded segment |

## Adding Cameras

1. Log in as admin
2. Dashboard → **+ Add Camera**
3. Name: URL-safe slug (e.g. `front-door`)
4. RTSP URL: `rtsp://user:pass@192.168.1.10:554/stream`

GoldenEye registers the camera with mediamtx immediately. Recording starts within seconds.

> **H.265 note:** Browser HLS playback of H.265 is limited (Safari only). Configure cameras to output H.264 for broadest compatibility.

## Dashboard

| Page | Path | Description |
|------|------|-------------|
| Live view | `/` | Camera grid; click **▶ Watch Live** to open HLS stream |
| Recordings | `/recordings` | Browse and play back recorded segments |
| Settings | Nav → Settings | Adjust retention and segment length (admin only) |

## Deployment

```bash
# Build static binary
task build   # outputs bin/goldeneye

# Copy to server
rsync bin/goldeneye bin/mediamtx mediamtx.yml config.json user@host:/app/goldeneye/

# Copy .env (adjust PSQL_URL host to localhost if Postgres is on the same machine)
scp .env user@host:/app/goldeneye/.env

# On the server — create systemd services
# See: /etc/systemd/system/mediamtx.service
#      /etc/systemd/system/goldeneye.service
sudo systemctl enable --now mediamtx goldeneye
```

If running on a port below 1024, add to the goldeneye service unit:
```ini
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
```

## Development

```bash
task dev          # mediamtx + Tailwind watch + go run (all in one)
task test         # go test ./...
task fmt          # go fmt ./...
```

### Schema changes

```bash
# 1. Edit schema.hcl
# 2. Apply to dev DB
task migrate-dev
# 3. Regenerate schema.sql
task generate-sql-schema
# 4. Regenerate sqlc queries
task sqlcgen
```

### Adding a SQL query

1. Add to `internal/sql/<feature>.sql` with sqlc annotation
2. `task sqlcgen`
3. Commit the `.sql` file and regenerated `sqlc/` together

## Security Notes

- RTSP URLs (including credentials) are stored plaintext in the database. Secure your DB and backups accordingly. AES-GCM encryption is a planned enhancement.
- mediamtx binds to `127.0.0.1` only. Do not expose ports 8554/8888/9997 externally.
- All live HLS streams are served through goldeneye's auth-gated proxy — direct mediamtx access is not possible from outside the host.
- `MASTER_KEY` rotation invalidates all active sessions (users must re-login). It does not affect stored passwords.
