---
name: dev-env-bringup
description: How to bring up the local no-TLS dev environment for GPUardian — the --dry-run node daemon on port 8193 and the dev web gateway on loopback 18080, with all state isolated under .dev/. Use whenever the user asks to start dev, run locally, test against the gateway, spin up a dev server, or do local development — even if they just say "let me test this locally" or "start the dev environment".
---

# Bring up the local dev environment

This is the only supported plaintext (no-TLS) workflow. It keeps all state
under `.dev/` (gitignored) and runs the node daemon with `--dry-run` so it
never enforces against real GPUs. Read `DEVELOPMENT.md` before changing any
dev port, path, or the `--dry-run` flow.

## 1. Build first

See the `dev-build-test` skill. At minimum:

```bash
npm --prefix web/ui ci && npm --prefix web/ui run build
go test ./...
go build -buildvcs=false -o gpuardian ./cmd/gpuardian
```

## 2. Create isolated dev state under `.dev/`

`.dev/` is gitignored and must stay out of `/var/lib/gpuardian` (prod).

```bash
mkdir -p .dev
# Per-node admin secret (rk_...) for the dev node
umask 077; printf 'rk_%s\n' "$(openssl rand -hex 32)" > .dev/root.key

# Dev gateway env (operator-only options)
cat > .dev/web.env <<'EOF'
GPUARDIAN_WEB_USER=admin
GPUARDIAN_WEB_PASSWORD=dev-admin-password-here
GPUARDIAN_WEB_ALLOW_REGISTRATION=1
EOF

# The dev Compose bind-mounts ./.dev/web as /var/lib/gpuardian-web inside the
# container; it must be owned by the non-root container UID/GID 65532.
sudo install -d -o 65532 -g 65532 -m 0700 .dev/web
```

## 3. Start the dev node daemon (port 8193, --dry-run, separate cgroup)

Run this in its own terminal and keep it open. Every `GPUARDIAN_*` path points
into `.dev/` so prod state is never touched.

```bash
sudo env \
  GPUARDIAN_NODE_ADDR=0.0.0.0:8193 \
  GPUARDIAN_NODE_ALLOW_INSECURE=1 \
  GPUARDIAN_SOCKET="$PWD/.dev/gpuardian.sock" \
  GPUARDIAN_STATE="$PWD/.dev/state.json" \
  GPUARDIAN_NODE_ID="$PWD/.dev/node.id" \
  GPUARDIAN_TELEMETRY_DIR="$PWD/.dev/telemetry" \
  GPUARDIAN_ROOT_KEY="$PWD/.dev/root.key" \
  GPUARDIAN_AUDIT_LOG="$PWD/.dev/audit.log" \
  GPUARDIAN_CGROUP_ROOT=/sys/fs/cgroup/gpuardian-dev \
  ./gpuardian daemon --dry-run
```

Verify it's listening:

```bash
sudo ss -lntp | grep -E ':8192|:8193'
```

## 4. Start the dev web gateway (loopback 18080)

```bash
sudo docker compose -f compose.web-dev.yml up -d --build
sudo docker compose -f compose.web-dev.yml ps
sudo docker compose -f compose.web-dev.yml logs --tail=100 gateway
```

Open `http://127.0.0.1:18080`. It's loopback-bound; for remote dev tunnel:
`ssh -L 18080:127.0.0.1:18080 <host>`.

## 5. Register the dev node in the web UI

In the gateway UI → `Nodes` tab → `Add node`:

- **Name**: `local-development`
- **Endpoint**: `http://host.docker.internal:8193`
  (Do **not** use `0.0.0.0` or `127.0.0.1` — inside the container those refer
  to the container itself. `host.docker.internal` reaches the host daemon.)
- **Root key**: contents of `.dev/root.key`
- **Skip TLS verify**: leave disabled (dev is plaintext HTTP, not self-signed
  TLS — the flag is for cert trust, not for HTTP).

## 6. Use the dev CLI against the dev socket

```bash
sudo env GPUARDIAN_SOCKET="$PWD/.dev/gpuardian.sock" ./gpuardian status
sudo env GPUARDIAN_SOCKET="$PWD/.dev/gpuardian.sock" ./gpuardian ps
```

## 7. Stop and reset

```bash
sudo docker compose -f compose.web-dev.yml down
# Ctrl-C the daemon in its terminal
sudo rm -rf .dev      # full reset of dev state
```

## CRITICAL invariants

- **Never run two enforcing daemons against the same GPUs.** The dev daemon
  must use `--dry-run` and `GPUARDIAN_CGROUP_ROOT=/sys/fs/cgroup/gpuardian-dev`.
  Production keeps `/sys/fs/cgroup/gpuardian`.
- **Dev state stays under `.dev/`.** Never remove or write to `/var/lib/gpuardian`,
  `/run/gpuardian.sock`, or the prod systemd service.
- **Dev and prod share `/var/lib/gpuardian-web` as the in-container bind mount
  path**, but dev points the host side at `.dev/web`. Don't confuse the two.

## Dev vs prod port/path map

| Thing | Prod | Dev |
|---|---|---|
| Node API | `8192` (HTTPS) | `8193` (HTTP) |
| Socket | `/run/gpuardian.sock` | `.dev/gpuardian.sock` |
| State | `/var/lib/gpuardian/state.json` | `.dev/state.json` |
| Node id | `/var/lib/gpuardian/node.id` | `.dev/node.id` |
| Telemetry | `/var/lib/gpuardian/telemetry` | `.dev/telemetry` |
| Root key | `/var/lib/gpuardian/root.key` | `.dev/root.key` |
| Cgroup root | `/sys/fs/cgroup/gpuardian` | `/sys/fs/cgroup/gpuardian-dev` |
| Enforcement | enabled | `--dry-run` |
| Gateway | `8443` (HTTPS) | `18080` (loopback HTTP) |

## Read before sensitive edits

- `DEVELOPMENT.md` — the canonical dev isolation rules.
- `AGENTS.md` — "Gotchas" section (two-daemon invariant, dev/prod paths).
