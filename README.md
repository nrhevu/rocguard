# RocGuard

RocGuard is a lightweight AMD GPU reservation and enforcement tool for shared
Linux servers.

It provides:

- a root daemon: `rocguard daemon`
- a user CLI: `rocguard run`, `rocguard allow`, `rocguard status`
- an optional web gateway: `rocguard web`
- scheduled reservation keys and flexible claim keys

RocGuard is monitor-kill enforcement, not kernel device isolation. Users with
root, sudo, or root-equivalent Docker access can bypass it.

## Requirements

- Linux with cgroup v2
- AMD ROCm tooling with `amd-smi`
- The latest patch release of a currently supported Go line. As of July 2026,
  use Go 1.26.5 (recommended) or Go 1.25.12. Check the
  [official release history](https://go.dev/doc/devel/release) for newer
  security patches. The `go 1.22` directive in `go.mod` is the source-language
  version, not a recommendation to build production binaries with an
  unsupported Go toolchain.
- The latest patch release of a currently supported Node.js LTS line, with npm,
  for building the web UI
- Root access for the daemon
- Optional: Docker, `crictl`, or `kubectl` for container scopes

## Build

```bash
npm --prefix web/ui ci
npm --prefix web/ui run build
go build -buildvcs=false -o rocguard ./cmd/rocguard
```

## Core Ideas

### Reserve

Use a reserved key when you know the GPU and time window in advance.

- Pick one or more GPUs.
- Pick `Start` and `End`.
- RocGuard returns a key.
- The key only works during that reservation window.
- Max reservation duration is 8 hours.

Run with:

```bash
KEY=rg_xxx rocguard run -- python train.py
```

### Claim

Use a claim key for flexible workflows without a fixed schedule.

- The key does not expire by schedule.
- When an authorized process starts using GPU memory, RocGuard claims that GPU.
- Other unauthorized processes on the claimed GPU are killed.
- If the GPU is already busy before the claim starts, RocGuard rejects the new
  claimed process and leaves the existing workload alone.

### Run vs Allow

Use `rocguard run` for normal commands:

```bash
KEY=rg_xxx rocguard run -- torchrun --nproc_per_node=8 train.py
```

Use `rocguard allow` when another system starts the workload:

```bash
KEY=rg_xxx rocguard allow docker --container trainer
KEY=rg_xxx rocguard allow k8s --namespace training
KEY=rg_xxx rocguard allow user --name alice
```

Do not rely on `rocguard run -- docker run ...` for Docker workloads. Docker
puts the real workload in a different container cgroup, so use
`rocguard allow docker` instead.

## Root Key

The root key is the admin secret for the local daemon. Regular users should not
need it.

Default path:

```text
/var/lib/rocguard/root.key
```

Create it once:

```bash
sudo install -d -o root -g root -m 0700 /var/lib/rocguard
sudo sh -c 'umask 077; test -f /var/lib/rocguard/root.key || printf "rk_%s\n" "$(openssl rand -hex 32)" > /var/lib/rocguard/root.key'
sudo chmod 600 /var/lib/rocguard/root.key
```

Read it as an admin:

```bash
sudo cat /var/lib/rocguard/root.key
```

If `ROCGUARD_ROOT_KEY` is set, use that file path instead.

## Installation

Choose the guide that matches the complete transport topology:

- [Install with TLS](INSTALL-TLS.md) — recommended for production. The node API
  and browser-facing gateway use HTTPS.
- [Install without TLS](INSTALL-NO-TLS.md) — plaintext HTTP for isolated,
  trusted development networks only.

Both guides include the build, binary and UI installation, node daemon, web
gateway, systemd hardening, first-admin setup, node registration, firewall
boundaries, and secret-file handling. Do not mix TLS and plaintext snippets:
RocGuard's three insecure-transport switches are independent and fail closed by
default.

For an existing deployment, follow the upgrade procedure below and then compare
its service configuration with the selected installation guide.

## Upgrade

Back up `/etc/rocguard`, `/var/lib/rocguard`, and `/var/lib/rocguard-web`, then
stop both services. Build the new version and replace the binary and UI. The UI
is staged on the destination filesystem, normalized to root ownership, and
swapped as a directory so removed asset bundles cannot linger:

```bash
sudo systemctl stop rocguard-web rocguard
npm --prefix web/ui ci
npm --prefix web/ui run build
go build -buildvcs=false -o rocguard ./cmd/rocguard
sudo install -o root -g root -m 0755 rocguard /usr/local/bin/rocguard
sudo install -d -o root -g root -m 0755 /usr/local/share/rocguard
UI_STAGE="$(sudo mktemp -d /usr/local/share/rocguard/.ui.XXXXXX)"
sudo cp -R web/ui/dist/. "$UI_STAGE/"
sudo chown -R root:root "$UI_STAGE"
sudo find "$UI_STAGE" -type d -exec chmod 0755 {} +
sudo find "$UI_STAGE" -type f -exec chmod 0644 {} +
sudo rm -rf /usr/local/share/rocguard/ui.previous
if sudo test -d /usr/local/share/rocguard/ui; then
  sudo mv /usr/local/share/rocguard/ui /usr/local/share/rocguard/ui.previous
fi
sudo mv "$UI_STAGE" /usr/local/share/rocguard/ui
sudo rm -rf /usr/local/share/rocguard/ui.previous
```

Older installations stored gateway state beside daemon state. Create the
dedicated account and migrate each legacy file only when its new destination
does not already exist:

```bash
if ! id rocguard-web >/dev/null 2>&1; then
  sudo useradd --system --user-group --home-dir /var/lib/rocguard-web --create-home --shell /usr/sbin/nologin rocguard-web
elif ! getent group rocguard-web >/dev/null 2>&1; then
  sudo groupadd --system rocguard-web
fi
sudo install -d -o rocguard-web -g rocguard-web -m 0700 /var/lib/rocguard-web
if sudo test -f /var/lib/rocguard/web-users.json && ! sudo test -e /var/lib/rocguard-web/users.json; then
  sudo install -o rocguard-web -g rocguard-web -m 0600 /var/lib/rocguard/web-users.json /var/lib/rocguard-web/users.json
fi
if sudo test -f /var/lib/rocguard/web-servers.json && ! sudo test -e /var/lib/rocguard-web/servers.json; then
  sudo install -o rocguard-web -g rocguard-web -m 0600 /var/lib/rocguard/web-servers.json /var/lib/rocguard-web/servers.json
fi
if sudo test -f /var/lib/rocguard/web-session.key && ! sudo test -e /var/lib/rocguard-web/session.key; then
  sudo install -o rocguard-web -g rocguard-web -m 0600 /var/lib/rocguard/web-session.key /var/lib/rocguard-web/session.key
fi
sudo find /var/lib/rocguard-web -maxdepth 1 -type f \( -name users.json -o -name servers.json -o -name session.key \) -exec chown rocguard-web:rocguard-web {} + -exec chmod 0600 {} +
```

Set `ROCGUARD_WEB_USERS`, `ROCGUARD_WEB_REGISTRY`, and
`ROCGUARD_WEB_SESSION_KEY` to those three new paths as shown in the gateway
service unit in [INSTALL-TLS.md](INSTALL-TLS.md) or
[INSTALL-NO-TLS.md](INSTALL-NO-TLS.md). If no legacy session key exists, the
gateway creates one on first start; that safely invalidates old browser
sessions. Do not copy either web state file over an existing destination.

Before starting the services, update their units for fail-closed transport
security:

- By default, a node API used by the built-in gateway must have
  `ROCGUARD_NODE_TLS_CERT` and `ROCGUARD_NODE_TLS_KEY`, even on loopback. A
  deliberate plaintext deployment instead requires both
  `ROCGUARD_NODE_ALLOW_INSECURE=1` on the node and
  `ROCGUARD_WEB_ALLOW_INSECURE_NODES=1` on the gateway. Leave
  `ROCGUARD_NODE_ADDR` empty when the API is not needed.
- A web gateway should have `ROCGUARD_WEB_TLS_CERT` and
  `ROCGUARD_WEB_TLS_KEY`. A deliberately plaintext reverse-proxy backend must
  bind to loopback and set both `ROCGUARD_WEB_ALLOW_INSECURE=1` and
  `ROCGUARD_WEB_SECURE_COOKIES=1`.
- A direct plaintext deployment must use all three independent opt-ins shown in
  [INSTALL-NO-TLS.md](INSTALL-NO-TLS.md):
  `ROCGUARD_NODE_ALLOW_INSECURE=1`,
  `ROCGUARD_WEB_ALLOW_INSECURE_NODES=1`, and
  `ROCGUARD_WEB_ALLOW_INSECURE=1`. Remove both certificate/key pairs together.
- User self-registration remains disabled unless the gateway explicitly sets
  `ROCGUARD_WEB_ALLOW_REGISTRATION=1`. Self-registered accounts always receive
  the regular user role.
- Remove `UMask=0077` from the daemon unit so it is not inherited by launched
  user workloads. The web gateway may keep its restrictive umask.

Then reload and start both services:

```bash
sudo systemctl daemon-reload
sudo systemctl start rocguard rocguard-web
sudo systemctl status rocguard rocguard-web --no-pager
```

After the gateway is reachable, make every registered node match the selected
installation guide. For TLS, replace each `http://` record with
`https://<node-host>:8192` and install the issuing CA instead of enabling
`Skip TLS verify`. For a deliberately plaintext deployment, set
`ROCGUARD_WEB_ALLOW_INSECURE_NODES=1` before restart and understand that every
stored HTTP record becomes active immediately.

Finally, sign in with each migrated administrator account, immediately change
any legacy/default password (including `change-me`), and review the `Users` tab
for accounts that should be removed. Delete `ROCGUARD_WEB_PASSWORD` from
`/etc/rocguard/web.env` and from any unit override after the first successful
sign-in, then restart the gateway to remove the bootstrap secret from its live
environment:

```bash
sudo sed -i '/^ROCGUARD_WEB_PASSWORD=/d' /etc/rocguard/web.env
sudo systemctl daemon-reload
sudo systemctl restart rocguard-web
```

Daemon state and root keys remain in `/var/lib/rocguard`; migrated gateway
state remains in `/var/lib/rocguard-web`.

## Uninstall

Stop the services and remove the installed program files:

```bash
sudo systemctl disable --now rocguard-web rocguard
sudo rm -f /etc/systemd/system/rocguard-web.service
sudo rm -f /etc/systemd/system/rocguard.service
sudo systemctl daemon-reload
sudo rm -f /usr/local/bin/rocguard
sudo rm -rf /usr/local/share/rocguard
sudo rm -f /run/rocguard.sock
```

The commands above keep configuration, users, keys, reservations, and logs so
RocGuard can be installed again later. To remove all RocGuard data permanently:

```bash
sudo rm -rf /etc/rocguard
sudo rm -rf /var/lib/rocguard
sudo rm -rf /var/lib/rocguard-web
sudo rm -rf /var/log/rocguard
sudo userdel rocguard-web
```

Also remove any firewall rules created for ports `8443`, `8080`, or `8192` (or
other ports chosen for your deployment).

## Quick Start

Start the daemon:

```bash
sudo ./rocguard daemon
```

Register a scheduled reservation key:

```bash
./rocguard register --reserved
```

Register a claim key:

```bash
./rocguard register --claimed
```

Run a command:

```bash
KEY=rg_xxx ./rocguard run -- python train.py
```

Check state:

```bash
KEY=rg_xxx ./rocguard status
KEY=rg_xxx ./rocguard ps
KEY=rg_xxx ./rocguard token info
ROOT_KEY=rk_xxx ./rocguard show-keys
```

## Docker, Kubernetes, and User Scopes

Docker:

```bash
KEY=rg_xxx rocguard allow docker --container trainer
KEY=rg_xxx rocguard allow docker --container 'trainer-*'
```

For exact Docker names, the container must already exist so RocGuard can resolve
it to an immutable container ID. Wildcards are matched against container names
during enforcement.

Kubernetes:

```bash
KEY=rg_xxx rocguard allow k8s --namespace training
KEY=rg_xxx rocguard allow k8s --namespace 'training-*'
```

User:

```bash
KEY=rg_xxx rocguard allow user --name alice
KEY=rg_xxx rocguard allow user --name 'team-*'
```

Keep `allow` scopes as narrow as possible. Wildcards created through the web
gateway require an admin account. A direct wildcard `rocguard allow` request on
the local Unix socket additionally requires the CLI caller to be root; regular
users can create exact scopes only.

## Admin Commands

Show stored keys:

```bash
ROOT_KEY=rk_xxx rocguard show-keys
```

Revoke a token, reservation, authorization, or bypass:

```bash
ROOT_KEY=rk_xxx rocguard revoke <id>
```

Bypass a trusted PID:

```bash
ROOT_KEY=rk_xxx rocguard bypass add --pid 1234 --ttl 24h --reason gpuagent
```

Bypass a trusted root-owned command. Command-path bypasses are restricted to
UID 0 because unprivileged mount namespaces can spoof executable pathnames;
use a PID bypass for non-root processes:

```bash
ROOT_KEY=rk_xxx rocguard bypass add --command /usr/bin/gpuagent --uid 0 --ttl 24h --reason gpuagent
```

## Command Reference

```text
rocguard help
rocguard daemon [--dry-run]
rocguard web [--addr <host:port>] [--registry <path>] [--ui-dir <path>]
rocguard register (--reserved | --claimed)
KEY=... rocguard run -- <command>
KEY=... rocguard allow docker --container <name-or-id>
KEY=... rocguard allow k8s --namespace <name>
KEY=... rocguard allow user --name <name>
KEY=... rocguard status  # root may omit KEY for the full node view
KEY=... rocguard ps      # root may omit KEY for the full node view
KEY=... rocguard token info
ROOT_KEY=... rocguard show-keys
ROOT_KEY=... rocguard bypass add (--pid <pid> | --command <path> --uid 0) --ttl <duration> --reason <text>
ROOT_KEY=... rocguard revoke <id>
```

## Configuration

Common environment variables:

```text
ROCGUARD_SOCKET=/run/rocguard.sock
ROCGUARD_STATE=/var/lib/rocguard/state.json
ROCGUARD_ROOT_KEY=/var/lib/rocguard/root.key
ROCGUARD_AUDIT_LOG=/var/log/rocguard/audit.log
ROCGUARD_NODE_ADDR=
ROCGUARD_NODE_TLS_CERT=
ROCGUARD_NODE_TLS_KEY=
ROCGUARD_NODE_ALLOW_INSECURE=0
ROCGUARD_WEB_ADDR=127.0.0.1:8080
ROCGUARD_WEB_TLS_CERT=
ROCGUARD_WEB_TLS_KEY=
ROCGUARD_WEB_ALLOW_INSECURE=0
ROCGUARD_WEB_ALLOW_INSECURE_NODES=0
ROCGUARD_WEB_ALLOW_REGISTRATION=0
ROCGUARD_WEB_SECURE_COOKIES=0
ROCGUARD_WEB_TRUST_PROXY=0
ROCGUARD_WEB_SESSION_KEY=/var/lib/rocguard/web-session.key
ROCGUARD_WEB_USER=admin
ROCGUARD_WEB_PASSWORD=
ROCGUARD_WEB_USERS=/var/lib/rocguard/web-users.json
ROCGUARD_WEB_REGISTRY=/var/lib/rocguard/web-servers.json
ROCGUARD_WEB_UI_DIR=web/ui/dist
ROCGUARD_GPU_COUNT=0
ROCGUARD_DRY_RUN=0
```

All insecure transport switches default to false. `ROCGUARD_NODE_ALLOW_INSECURE`
and `ROCGUARD_WEB_ALLOW_INSECURE` govern the node and browser-facing plaintext
listeners, including loopback. `ROCGUARD_WEB_ALLOW_INSECURE_NODES` separately
governs outbound gateway-to-node HTTP. `ROCGUARD_WEB_SECURE_COOKIES` forces the
browser cookie's `Secure` flag when HTTPS terminates at a reverse proxy. Native
gateway TLS sets that flag automatically.
`ROCGUARD_WEB_ALLOW_REGISTRATION` shows the public `Create account` flow and
permits rate-limited regular-user registration; it never permits an account to
select the administrator role. Because every registered user can reserve GPUs,
enable it only for controlled onboarding or a gateway intentionally open to all
reachable users.
`ROCGUARD_WEB_TRUST_PROXY` should be enabled only for a loopback reverse proxy
that overwrites `X-Forwarded-For`; it prevents all proxied clients from sharing
one login-rate-limit bucket.

Development-only overrides:

```text
ROCGUARD_CGROUP_ROOT
ROCGUARD_PROC_ROOT
```

## Development

Run tests:

```bash
GOCACHE=/tmp/rocguard-go-build go test ./...
```

Build the UI:

```bash
npm --prefix web/ui run build
```

Build the CLI:

```bash
GOCACHE=/tmp/rocguard-go-build go build -buildvcs=false -o rocguard ./cmd/rocguard
```

## Safety Notes

- Do not test on production GPUs with active workloads unless you are ready for
  unauthorized processes to be killed.
- Keep Docker socket access restricted. The `docker` group is effectively
  root-equivalent.
- Do not share the root key with regular users.
- Revoke keys and reservations that are no longer needed.

## License

RocGuard is licensed under the [Apache License 2.0](LICENSE).
