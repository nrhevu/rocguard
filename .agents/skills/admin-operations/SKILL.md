---
name: admin-operations
description: How to operate a running GPUardian deployment as an admin — manage user accounts and roles, rotate fixed keys, run ROOT_KEY CLI operations (show-keys, bypass add, revoke, register), drive the gateway Compose service, and uninstall. Use whenever the user asks to add or remove a user, rotate a key, bypass enforcement, revoke something, restart the gateway, or uninstall GPUardian — even if they just say "create a user for alice" or "restart the web service".
---

# Operate a running GPUardian deployment

These are day-2 operations on an already-installed deployment (see the
`prod-install-gateway` and `prod-install-node` skills for initial install).

## User and account management (web UI + web.env)

### Enable / disable self-registration

Toggle in `/etc/gpuardian/web.env` and recreate the container:

```bash
# Enable
sudo sed -i 's/^GPUARDIAN_WEB_ALLOW_REGISTRATION=.*/GPUARDIAN_WEB_ALLOW_REGISTRATION=1/' /etc/gpuardian/web.env
# Disable
sudo sed -i 's/^GPUARDIAN_WEB_ALLOW_REGISTRATION=.*/GPUARDIAN_WEB_ALLOW_REGISTRATION=0/' /etc/gpuardian/web.env
sudo docker compose -f compose.web.yml up -d --force-recreate
```

Self-registered accounts are **always regular users** (never admin), even when
registration is open. Password must be ≥ 12 bytes.

### Create accounts when registration is disabled

In the gateway UI → `Users` tab (admin only). Or `POST /api/users` with
`{"username","password","role"}`. Role defaults to `user` if omitted.

### Roles

- **admin** — sees and can act on all users' resources.
- **user** (regular) — sees only their own resources; the gateway enforces
  this server-side.

### Rotate a user's fixed key

In the web UI → `Key` tab → **`Regenerate`**. This invalidates the previous
`gk_...` key **after the managed-key snapshot reaches each node** (not
instant). Admins can `Show key` for any user; regular users can only
show/regenerate their own.

## Admin CLI operations (ROOT_KEY)

These use the **per-node root key** (`rk_...`), not a user's `gk_...` key.
`ROOT_KEY` is read from env or prompted on stdin if unset.

```bash
# List fixed keys synced to a node (diagnostic)
ROOT_KEY=rk_xxx gpuardian show-keys

# Bypass enforcement for a specific PID (preferred)
ROOT_KEY=rk_xxx gpuardian bypass add --pid <pid> --ttl 30m --reason "maintenance"

# Bypass for a command path (UID-0 ONLY — unprivileged mount namespaces can
# spoof executable paths)
ROOT_KEY=rk_xxx gpuardian bypass add --command /usr/bin/nvidia-smi --uid 0 --ttl 1h --reason "monitoring"

# Revoke a token / reservation / authorization / bypass by ID
ROOT_KEY=rk_xxx gpuardian revoke <id>
```

### `bypass add` rules

- Exactly one of `--pid <pid>` or `--command <abs-path>`.
- `--command` is restricted to `--uid 0` (enforced in code).
- `--ttl` defaults to `2h`.
- `--reason` is **required**.
- No positional args.
- **Prefer PID bypasses.** Command-path bypasses are UID-0 only because
  unprivileged mount namespaces can spoof executable paths.

### `register` (interactive, node-side token registration)

```bash
gpuardian register --reserved    # or --claimed
```

Prompts for `Root key:`, `Name:`, and (for `--reserved`) `GPUs:` + `TTL [2h]:`.
This is the legacy/CLI way to create a reservation token directly on a node
using `ROOT_KEY`. It does **not** read `ROOT_KEY` from env — it prompts.
Requires exactly one of `--reserved` or `--claimed`.

## Gateway Compose operations

```bash
sudo docker compose -f compose.web.yml ps
sudo docker compose -f compose.web.yml logs -f gateway
sudo docker compose -f compose.web.yml restart gateway
sudo docker compose -f compose.web.yml down
sudo docker compose -f compose.web.yml up -d
```

The bind-mounted `/var/lib/gpuardian-web` (including `history.db`) persists
across container recreate/remove. See the `gateway-backup-restore` skill for
the backup contract.

## Uninstall

### Gateway

```bash
sudo docker compose -f compose.web.yml down
```

### Each node

```bash
sudo systemctl disable --now gpuardian
sudo rm -f /etc/systemd/system/gpuardian.service
sudo systemctl daemon-reload
sudo rm -f /usr/local/bin/gpuardian
sudo rm -f /run/gpuardian.sock
```

This retains config, state, keys, and logs under `/etc/gpuardian`,
`/var/lib/gpuardian`, `/var/log/gpuardian`.

### Full data removal (destructive)

```bash
sudo rm -rf /etc/gpuardian /var/lib/gpuardian /var/lib/gpuardian-web /var/log/gpuardian
# plus remove firewall rules for 8192/8443
```

## Gotchas

- **`ROOT_KEY` vs `KEY`**: `ROOT_KEY=rk_...` is admin (per-node);
  `KEY=gk_...` is a regular user's fixed key. `show-keys`, `bypass add`,
  `revoke` use `ROOT_KEY`; `run`, `allow`, `status`, `ps`, `token info` use
  `KEY`. Don't confuse them.
- **`register` is node-side token registration, not account creation.**
  Accounts are web-API-only (`/api/register` or `/api/users`). `register`
  creates a reservation token on a node via the daemon socket.
- **Regenerating a key is not instant.** The old `gk_...` works until the
  managed-key snapshot reaches each node. If you're rotating because of a
  leak, revoke the user's active reservations too.
- **Never log `rk_...` or `gk_...`.** Audit log path is `GPUARDIAN_AUDIT_LOG`.

## Read before sensitive edits

- `README.md` — CLI reference, env-var list, operations section.
- `cmd/gpuardian/main.go` — subcommand dispatch and the KEY-vs-ROOT_KEY split.
- `AGENTS.md` — "Command-path bypasses are UID-0 only" and key-prefix
  conventions.
