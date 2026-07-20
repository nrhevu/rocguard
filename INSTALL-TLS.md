# Install RocGuard with TLS

This is the recommended production installation. The node API and the web
gateway both use HTTPS, so login credentials, session cookies, node root keys,
and control requests are encrypted in transit.

For an isolated development network where you intentionally accept plaintext
traffic, use [INSTALL-NO-TLS.md](INSTALL-NO-TLS.md) instead.

## Requirements

- Linux with systemd and cgroup v2
- AMD ROCm tooling with `amd-smi` on every GPU node
- Root access for installing and running the node daemon
- A currently supported Go toolchain and Node.js LTS with npm for building
- OpenSSL for generating node root keys
- TLS certificates and keys in PEM format

See [README.md](README.md#requirements) for the currently recommended Go and
Node.js versions.

## Topology

- Run `rocguard daemon` as root on every GPU node.
- Expose each required node API as `https://<node-host>:8192` only to the
  gateway host.
- Run one `rocguard web` gateway as the dedicated `rocguard-web` account.
- Expose the gateway as `https://<gateway-host>:8443` only to intended users.
- Keep node root keys on their nodes and in the gateway's private registry.

If a node does not need to be managed by a gateway, leave
`ROCGUARD_NODE_ADDR` empty and do not expose port `8192`.

## 1. Build RocGuard

From the repository root on a trusted build host:

```bash
npm --prefix web/ui ci
npm --prefix web/ui run build
go build -buildvcs=false -o rocguard ./cmd/rocguard
```

Install the binary on every GPU node and on the gateway host. Install the UI on
the gateway host:

```bash
sudo install -o root -g root -m 0755 rocguard /usr/local/bin/rocguard
sudo install -d -o root -g root -m 0755 /etc/rocguard
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

The UI copy is not needed on a GPU node that does not also run the gateway.

## 2. Obtain TLS certificates

Use certificates issued by your organization's internal CA or a public CA.
Each `.crt` file should be a full-chain PEM containing the server certificate
followed by any required intermediate certificates. Each certificate must
permit TLS server authentication and contain the exact DNS name or IP address
clients use in its Subject Alternative Name (SAN):

- A node certificate must match the host in
  `https://<node-host>:8192`.
- The gateway certificate must match the host users open in their browsers.

Install the CA that issued each node certificate in the gateway host's system
trust store. Install the CA that issued the gateway certificate in browser and
client trust stores. Keep `Skip TLS verify` disabled when registering nodes. A
development self-signed certificate can be generated with OpenSSL, but clients
must either trust it explicitly or use `Skip TLS verify`, which removes server
identity verification and is not appropriate for production.

Install each node's certificate and private key on that node:

```bash
sudo install -o root -g root -m 0644 /path/to/node.crt /etc/rocguard/node.crt
sudo install -o root -g root -m 0600 /path/to/node.key /etc/rocguard/node.key
```

Never copy `node.key` to the gateway. The gateway needs only the CA trust chain
for the node certificate.

For a strict TLS deployment, remove or set these legacy plaintext overrides to
false wherever they may exist in unit files, drop-ins, and environment files:

```text
ROCGUARD_NODE_ALLOW_INSECURE
ROCGUARD_WEB_ALLOW_INSECURE_NODES
ROCGUARD_WEB_ALLOW_INSECURE
```

Register every node with an `https://` endpoint. The optional reverse-proxy
topology at the end of this guide is the only case here that deliberately uses
`ROCGUARD_WEB_ALLOW_INSECURE=1` for a loopback backend.

## 3. Install each node daemon

Create private state and a unique root key on every GPU node:

```bash
sudo install -d -o root -g root -m 0700 /var/lib/rocguard
sudo install -d -o root -g root -m 0700 /var/log/rocguard
sudo sh -c 'umask 077; test -f /var/lib/rocguard/root.key || printf "rk_%s\n" "$(openssl rand -hex 32)" > /var/lib/rocguard/root.key'
sudo chmod 0600 /var/lib/rocguard/root.key
```

Create `/etc/systemd/system/rocguard.service`:

```ini
[Unit]
Description=RocGuard daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
Group=root
Environment=ROCGUARD_SOCKET=/run/rocguard.sock
Environment=ROCGUARD_STATE=/var/lib/rocguard/state.json
Environment=ROCGUARD_ROOT_KEY=/var/lib/rocguard/root.key
Environment=ROCGUARD_AUDIT_LOG=/var/log/rocguard/audit.log
Environment=ROCGUARD_NODE_ADDR=0.0.0.0:8192
Environment=ROCGUARD_NODE_TLS_CERT=/etc/rocguard/node.crt
Environment=ROCGUARD_NODE_TLS_KEY=/etc/rocguard/node.key
ExecStart=/usr/local/bin/rocguard daemon
Restart=always
RestartSec=2
NoNewPrivileges=true
LockPersonality=true
ProtectClock=true
ProtectHostname=true
ProtectKernelLogs=true
ProtectKernelModules=true
ProtectKernelTunables=true
RestrictSUIDSGID=true
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
```

Enable and verify the node:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now rocguard
sudo systemctl status rocguard --no-pager
sudo rocguard status
```

The Unix socket `/run/rocguard.sock` remains available for local CLI use.
Non-root status and process queries require a valid user `KEY` and return only
that key's records; root can inspect the full node.

Allow TCP port `8192` only from the gateway host. The root bearer key is still
required for the node API; TLS protects that key while it crosses the network.

The daemon must inspect `/proc`, access AMD devices, manage its cgroup subtree,
and launch user workloads. Do not add a service-wide restrictive `UMask` or
systemd settings such as `PrivateDevices`, `ProtectControlGroups`,
`ProtectHome`, or `ProtectSystem` without testing GPU enforcement and
`rocguard run` end to end. Those settings also affect launched workloads.

## 4. Install the web gateway

Run this section once on the gateway host. Create a dedicated account and
private state directory:

```bash
sudo useradd --system --user-group --home-dir /var/lib/rocguard-web --create-home --shell /usr/sbin/nologin rocguard-web
sudo install -d -o rocguard-web -g rocguard-web -m 0700 /var/lib/rocguard-web
sudo install -o root -g root -m 0600 /dev/null /etc/rocguard/web.env
```

If the account already exists, omit the `useradd` command.

Install the gateway certificate and private key:

```bash
sudo install -o root -g root -m 0644 /path/to/web.crt /etc/rocguard/web.crt
sudo install -o root -g rocguard-web -m 0640 /path/to/web.key /etc/rocguard/web.key
```

Generate a unique bootstrap password and save it in the root-only environment
file:

```bash
WEB_PASSWORD="$(openssl rand -hex 32)"
printf 'ROCGUARD_WEB_USER=admin\nROCGUARD_WEB_PASSWORD=%s\n' "$WEB_PASSWORD" | sudo tee /etc/rocguard/web.env >/dev/null
printf 'Initial RocGuard admin password: %s\n' "$WEB_PASSWORD"
unset WEB_PASSWORD
```

Store the displayed password in an approved password manager. Create
`/etc/systemd/system/rocguard-web.service`:

```ini
[Unit]
Description=RocGuard web gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=rocguard-web
Group=rocguard-web
EnvironmentFile=/etc/rocguard/web.env
Environment=ROCGUARD_WEB_ADDR=0.0.0.0:8443
Environment=ROCGUARD_WEB_TLS_CERT=/etc/rocguard/web.crt
Environment=ROCGUARD_WEB_TLS_KEY=/etc/rocguard/web.key
Environment=ROCGUARD_WEB_USERS=/var/lib/rocguard-web/users.json
Environment=ROCGUARD_WEB_REGISTRY=/var/lib/rocguard-web/servers.json
Environment=ROCGUARD_WEB_SESSION_KEY=/var/lib/rocguard-web/session.key
Environment=ROCGUARD_WEB_UI_DIR=/usr/local/share/rocguard/ui
ExecStart=/usr/local/bin/rocguard web
Restart=always
RestartSec=2
UMask=0077
NoNewPrivileges=true
PrivateDevices=true
PrivateTmp=true
ProtectClock=true
ProtectControlGroups=true
ProtectHome=true
ProtectHostname=true
ProtectKernelLogs=true
ProtectKernelModules=true
ProtectKernelTunables=true
ProtectSystem=strict
ReadWritePaths=/var/lib/rocguard-web
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
SystemCallArchitectures=native
CapabilityBoundingSet=
AmbientCapabilities=

[Install]
WantedBy=multi-user.target
```

Enable the gateway:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now rocguard-web
sudo systemctl status rocguard-web --no-pager
```

Allow TCP port `8443` only from networks that should access the UI. Open
`https://<gateway-host>:8443`, sign in, and immediately change the bootstrap
password. Then remove it from the service environment:

```bash
sudo sed -i '/^ROCGUARD_WEB_PASSWORD=/d' /etc/rocguard/web.env
sudo systemctl restart rocguard-web
```

The first admin is created only when the users file is empty. New passwords
must contain at least 12 bytes. Administrators can manage every user and key;
regular users can manage only their own keys, reservations, and exact
authorization scopes. Claim-key creation and wildcard authorization scopes are
admin-only in the gateway.

### Optional: enable user self-registration

Self-registration is disabled by default. To show `Create account` on the sign-in
screen, add this line to the gateway service's `[Service]` section:

```ini
Environment=ROCGUARD_WEB_ALLOW_REGISTRATION=1
```

Reload and restart the gateway:

```bash
sudo systemctl daemon-reload
sudo systemctl restart rocguard-web
```

Self-registered accounts are always regular users and are signed in
automatically after creation. They cannot request the administrator role.
Every registered user can reserve GPUs, so enable this only during controlled
onboarding or when the gateway is intentionally open to all reachable users.
Remove the setting or set it to `0` afterward to return to
administrator-created accounts only.

## 5. Register the nodes

Read a node's root key as an administrator on that node:

```bash
sudo cat /var/lib/rocguard/root.key
```

In the gateway's `Nodes` tab, add:

```text
Name: a display name
Endpoint API: https://<node-host>:8192
Root key: contents of /var/lib/rocguard/root.key on that node
Skip TLS verify: disabled
```

If the connection fails with an unknown-authority error, install the issuing CA
on the gateway host instead of disabling verification. If it fails with a
hostname error, issue a certificate whose SAN matches the endpoint hostname or
IP.

## 6. Protect and back up gateway state

The gateway creates a random session-signing key on first start. The following
files contain secrets and must remain owned by `rocguard-web`, mode `0600`:

```text
/var/lib/rocguard-web/session.key
/var/lib/rocguard-web/servers.json
/var/lib/rocguard-web/users.json
```

Back them up according to your secret-management policy. Losing or rotating
`session.key` signs out all active browser sessions. Never replace it with the
bootstrap password.

## Optional: terminate browser TLS at a reverse proxy

Native TLS between the reverse proxy and RocGuard is preferred. If a dedicated,
trusted gateway host intentionally uses a plaintext loopback backend, replace
the gateway address and TLS settings with:

```ini
Environment=ROCGUARD_WEB_ADDR=127.0.0.1:8080
Environment=ROCGUARD_WEB_ALLOW_INSECURE=1
Environment=ROCGUARD_WEB_SECURE_COOKIES=1
Environment=ROCGUARD_WEB_TRUST_PROXY=1
```

Remove both `ROCGUARD_WEB_TLS_CERT` and `ROCGUARD_WEB_TLS_KEY`. Configure the
proxy to accept only HTTPS, preserve the original `Host`, add HSTS, and replace
`X-Forwarded-For` with the client address instead of appending to it. For nginx:

```nginx
proxy_set_header Host $host;
proxy_set_header X-Forwarded-For $remote_addr;
```

The immediate proxy peer must be loopback. A local user can impersonate a
stopped plaintext backend, so use this topology only on a dedicated trusted
host.

After installation, see [User-Guide.md](User-Guide.md) for normal user
workflows and [README.md](README.md) for upgrades, configuration, and command
reference.
