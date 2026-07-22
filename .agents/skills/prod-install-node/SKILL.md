---
name: prod-install-node
description: How to install the GPUardian node daemon in production on an AMD or NVIDIA GPU server — build the binary, stage TLS certificates, generate the per-node root key, write the hardened systemd unit, and verify it listens on 8192. Use whenever the user asks to install on a node, set up the daemon, deploy to a GPU server, or configure a production node — even if they just say "put GPUardian on this machine" or "set up the node".
---

# Install the node daemon in production

Run on **each GPU node** (AMD or NVIDIA), at the same repo revision as the
gateway. Requires root on the node. The daemon must run on the host, never in
a container. Read `README.md` production install before touching this flow.

## 1. Build and install the binary

```bash
go test ./...
go build -buildvcs=false -o gpuardian ./cmd/gpuardian
sudo install -o root -g root -m 0755 gpuardian /usr/local/bin/gpuardian
sudo install -d -o root -g root -m 0755 /etc/gpuardian
```

## 2. Stage and install TLS certificates

Each node needs its own cert with a SAN matching the node's DNS name or IP
(the gateway will connect to `https://<node>:8192` and verifies the hostname).

Stage to `/tmp/gpuardian-install/` then install:

```bash
sudo install -o root -g root -m 0644 /tmp/gpuardian-install/node.crt /etc/gpuardian/node.crt
sudo install -o root -g root -m 0600 /tmp/gpuardian-install/node.key /etc/gpuardian/node.key
```

Each `.crt` = server cert + intermediates. **Never copy a node's `node.key`
to the gateway host.**

On the **gateway host** (not each node), install the node-issuing CA into the
system trust store so the gateway container trusts node certs:
Debian/Ubuntu — copy to `/usr/local/share/ca-certificates/gpuardian-ca.crt`,
then `sudo update-ca-certificates`. The prod Compose mounts the host's
`/etc/ssl/certs/ca-certificates.crt` into the container; on other distros,
update the Compose mount to that distro's CA bundle path.

## 3. Generate the per-node root key

```bash
sudo install -d -o root -g root -m 0700 /var/lib/gpuardian
sudo sh -c 'umask 077; test -f /var/lib/gpuardian/root.key || \
  printf "rk_%s\n" "$(openssl rand -hex 32)" > /var/lib/gpuardian/root.key'
sudo chmod 0600 /var/lib/gpuardian/root.key
```

`rk_...` is the per-node admin secret. The gateway uses it to talk to the
node; admin CLI subcommands (`show-keys`, `bypass add`, `revoke`) use it via
`ROOT_KEY`. **Never log it.** You'll paste it into the gateway's `Add node`
form later (see the `prod-install-gateway` skill).

## 4. Write the systemd unit

Create `/etc/systemd/system/gpuardian.service`. The exact unit is in
`README.md` (production install) — read it for the authoritative version.
The key fields:

- `ExecStart=/usr/local/bin/gpuardian daemon`
- `Environment=` lines for: `GPUARDIAN_SOCKET=/run/gpuardian.sock`,
  `GPUARDIAN_STATE=/var/lib/gpuardian/state.json`,
  `GPUARDIAN_NODE_ID=/var/lib/gpuardian/node.id`,
  `GPUARDIAN_TELEMETRY_DIR=/var/lib/gpuardian/telemetry`,
  `GPUARDIAN_ROOT_KEY=/var/lib/gpuardian/root.key`,
  `GPUARDIAN_AUDIT_LOG=/var/log/gpuardian/audit.log`,
  `GPUARDIAN_NODE_ADDR=0.0.0.0:8192`,
  `GPUARDIAN_NODE_TLS_CERT=/etc/gpuardian/node.crt`,
  `GPUARDIAN_NODE_TLS_KEY=/etc/gpuardian/node.key`.
- Hardening: `NoNewPrivileges`, `LockPersonality`,
  `ProtectClock`/`ProtectHostname`/`ProtectKernelLogs`/`ProtectModules`/
  `ProtectTunables`, `RestrictSUIDSGID`, `SystemCallArchitectures=native`.

**Do not add** `PrivateDevices`, `ProtectControlGroups`, `ProtectHome`,
`ProtectSystem`, or a restrictive `UMask` without testing `gpuardian run`
and enforcement end-to-end — the daemon needs `/proc`, `/sys/fs/cgroup`, and
the GPU device paths.

Create the log dir:

```bash
sudo install -d -o root -g root -m 0755 /var/log/gpuardian
```

## 5. Enable, start, and verify

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now gpuardian
sudo systemctl status gpuardian --no-pager
sudo gpuardian status
sudo ss -lntp | grep ':8192'
```

## 6. Firewall and vendor config

- Allow **TCP 8192 only from the gateway host** — do not expose it to user or
  public networks.
- Set `GPUARDIAN_GPU_VENDOR` if the node has both `amd-smi` and `nvidia-smi`
  installed (rare). It defaults to `auto` (probes `amd-smi` first, then
  `nvidia-smi`). The daemon never opens `/dev/nvidia*`, `/dev/kfd`, or
  `/dev/dri` directly — container device passthrough is the caller's job.

## Gotchas

- **Never run two enforcing daemons against the same GPUs.** One daemon per
  node, one cgroup root (`/sys/fs/cgroup/gpuardian` in prod).
- **The daemon is host-only.** It reads `/proc`, `/sys/fs/cgroup`, and the
  vendor's SMI CLI. Never add container-only assumptions.
- **`rk_...` is a per-node admin secret.** Never log it, never put it in
  source control, never copy a node's `node.key` to the gateway.
- **Cert SAN must match the hostname the gateway will dial.** A mismatch
  causes a certificate hostname error when registering the node.

## Read before sensitive edits

- `README.md` — production install (the authoritative systemd unit, cert
  staging, env-var list).
- `AGENTS.md` — "Daemon must stay host-only" and "Never run two enforcing
  daemons" boundaries.
