---
name: gateway-backup-restore
description: How to back up and restore GPUardian gateway state — which files under /var/lib/gpuardian-web are secrets, how to back up the SQLite history.db safely (WAL-aware), and the restore contract. Use whenever the user asks to back up, restore, migrate, move, or disaster-recover the gateway — even if they just say "copy the gateway state" or "move GPUardian to a new host".
---

# Back up and restore gateway state

The gateway's persistent state lives under `/var/lib/gpuardian-web` (bind-mounted
into the container). Some of these files are **secrets** and must be backed up
as such; `history.db` is SQLite and needs WAL-aware handling. Read
`README.md` backup contract before touching this flow.

## What to back up

All files are owned by UID/GID `65532`, mode `0600`, under
`/var/lib/gpuardian-web`:

| File | What it holds | If lost |
|---|---|---|
| `session.key` | Session-signing key | All browser sessions invalidated (users signed out) |
| `user-key.key` | Master key encrypting fixed keys in `users.json` | **Fixed keys in `users.json` become unrecoverable** — MUST back up together with `users.json` |
| `servers.json` | Registered nodes | Nodes must be re-registered |
| `users.json` | Accounts + encrypted fixed keys | Accounts lost |
| `history.db` | Reservation history (SQLite) | History lost |

**`user-key.key` and `users.json` must be backed up and restored together.**
Losing `user-key.key` alone makes the encrypted fixed keys in `users.json`
unrecoverable.

## Back up

### Secrets (session.key, user-key.key, servers.json, users.json)

File copy is safe for these (they're not WAL-backed):

```bash
sudo install -d -o 65532 -g 65532 -m 0700 /backup/gpuardian-web
sudo cp -p /var/lib/gpuardian-web/{session.key,user-key.key,servers.json,users.json} /backup/gpuardian-web/
sudo chown 65532:65532 /backup/gpuardian-web/*
sudo chmod 0600 /backup/gpuardian-web/*
```

### history.db (SQLite — WAL-aware)

`modernc.org/sqlite` uses WAL; a live file copy may miss committed data still
in the WAL. **While the gateway is running**, use the SQLite Online Backup
API or `VACUUM INTO`:

```bash
# If you have sqlite3 on the host (the gateway uses modernc.org/sqlite, but
# the on-disk format is standard SQLite and readable by the sqlite3 CLI):
sudo sqlite3 /var/lib/gpuardian-web/history.db "VACUUM INTO '/backup/gpuardian-web/history.db'"
```

A plain filesystem copy of `history.db` is safe **only after a clean stop**
of the gateway (`docker compose down`), because that's the only time the WAL
is guaranteed quiescent.

## Restore

1. **Stop the gateway first.** Restore only while stopped.
   ```bash
   sudo docker compose -f compose.web.yml down
   ```
2. Copy the backed-up files back, preserving ownership and permissions:
   ```bash
   sudo cp -p /backup/gpuardian-web/{session.key,user-key.key,servers.json,users.json,history.db} /var/lib/gpuardian-web/
   sudo chown 65532:65532 /var/lib/gpuardian-web/{session.key,user-key.key,servers.json,users.json,history.db}
   sudo chmod 0600 /var/lib/gpuardian-web/{session.key,user-key.key,servers.json,users.json,history.db}
   ```
3. Restart:
   ```bash
   sudo docker compose -f compose.web.yml up -d
   ```

## Storage and security rules

- **Keep backups on a local filesystem.** NFS is not supported (SQLite WAL
  semantics + file locking).
- **Never put these files in source control.** This includes `session.key`,
  `user-key.key`, `users.json`, `servers.json`, `history.db`, node root keys
  (`rk_...`), cert private keys, and `web.env`. They're all gitignored or
  operator-only.
- **Back up `user-key.key` and `users.json` together.** Restoring one without
  the other leaves fixed keys unrecoverable.
- **Preserve UID/GID 65532 and mode 0600** on restore — the container runs
  non-root and the files must be readable by that UID.

## Gotchas

- **`modernc.org/sqlite` requires no CGO**, but its transactions and WAL
  semantics still apply. Don't assume a live file copy of `history.db` is
  consistent — use `VACUUM INTO` or stop the gateway first.
- **`session.key` loss is recoverable** (users just re-sign-in) but
  disruptive. `user-key.key` loss is **not** recoverable without a paired
  `users.json` restore — treat it as the most critical secret.
- **The bind mount persists across container recreate/remove.** `docker
  compose down` does **not** delete `/var/lib/gpuardian-web` — only
  `docker compose down -v` would, and the prod Compose uses a bind mount (not
  a named volume), so `-v` doesn't apply. Still, back up before any
  destructive operation.

## Read before sensitive edits

- `README.md` — backup contract for gateway state.
- `AGENTS.md` — "`modernc.org/sqlite` requires no CGO" and "Secrets never go
  in source control" conventions.
