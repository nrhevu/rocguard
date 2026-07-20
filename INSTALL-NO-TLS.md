# Install RocGuard without TLS

> **Warning:** This installation is plaintext end to end. Use it only for local
> development or on an isolated, trusted network. Never expose it to the public
> Internet or an untrusted LAN.

HTTP exposes gateway login passwords, session cookies, node root bearer keys,
and every control request to interception and modification. Authentication and
firewall rules reduce access but do not provide encryption or server identity
verification. Use [INSTALL-TLS.md](INSTALL-TLS.md) for production.

## Requirements

- Linux with systemd and cgroup v2
- AMD ROCm tooling with `amd-smi` on every GPU node
- Root access for installing and running the node daemon
- A currently supported Go toolchain and Node.js LTS with npm for building
- OpenSSL for generating node root keys

See [README.md](README.md#requirements) for the currently recommended Go and
Node.js versions.

## Required plaintext opt-ins

RocGuard fails closed unless all relevant plaintext transports are explicitly
enabled. This guide uses three independent settings:

```text
ROCGUARD_NODE_ALLOW_INSECURE=1       node API accepts HTTP
ROCGUARD_WEB_ALLOW_INSECURE_NODES=1 gateway contacts HTTP nodes
ROCGUARD_WEB_ALLOW_INSECURE=1        browsers contact gateway over HTTP
```

`ROCGUARD_WEB_ALLOW_INSECURE` does not enable HTTP node endpoints, and
`ROCGUARD_WEB_ALLOW_INSECURE_NODES` does not enable the browser-facing HTTP
listener. Enabling insecure nodes makes every stored `http://` node record
contactable immediately.

Do not set only one member of a TLS certificate/key pair. This guide omits all
node and gateway TLS certificate/key settings.

## Topology

- Run `rocguard daemon` as root on every GPU node.
- Expose `http://<node-host>:8192` only to the gateway host.
- Run one `rocguard web` gateway as the dedicated `rocguard-web` account.
- Expose `http://<gateway-host>:8080` only to intended users on a trusted
  management network.

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

## 2. Install each plaintext node daemon

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
Description=RocGuard daemon (plaintext node API)
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
Environment=ROCGUARD_NODE_ALLOW_INSECURE=1
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

There must be no `ROCGUARD_NODE_TLS_CERT` or `ROCGUARD_NODE_TLS_KEY` setting in
this unit or an inherited environment file.

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

Use a host firewall or security group to allow TCP port `8192` only from the
gateway host. The root bearer key travels over this connection in plaintext.

The daemon must inspect `/proc`, access AMD devices, manage its cgroup subtree,
and launch user workloads. Do not add a service-wide restrictive `UMask` or
systemd settings such as `PrivateDevices`, `ProtectControlGroups`,
`ProtectHome`, or `ProtectSystem` without testing GPU enforcement and
`rocguard run` end to end. Those settings also affect launched workloads.

## 3. Install the plaintext web gateway

Run this section once on the gateway host. Create a dedicated account and
private state directory:

```bash
sudo useradd --system --user-group --home-dir /var/lib/rocguard-web --create-home --shell /usr/sbin/nologin rocguard-web
sudo install -d -o rocguard-web -g rocguard-web -m 0700 /var/lib/rocguard-web
sudo install -o root -g root -m 0600 /dev/null /etc/rocguard/web.env
```

If the account already exists, omit the `useradd` command.

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
Description=RocGuard web gateway (plaintext HTTP)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=rocguard-web
Group=rocguard-web
EnvironmentFile=/etc/rocguard/web.env
Environment=ROCGUARD_WEB_ADDR=0.0.0.0:8080
Environment=ROCGUARD_WEB_ALLOW_INSECURE=1
Environment=ROCGUARD_WEB_ALLOW_INSECURE_NODES=1
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

There must be no `ROCGUARD_WEB_TLS_CERT` or `ROCGUARD_WEB_TLS_KEY` setting in
this unit or its environment file. Do not set `ROCGUARD_WEB_SECURE_COOKIES=1`
for direct HTTP: browsers would refuse to return the session cookie over HTTP.
Leave `ROCGUARD_WEB_TRUST_PROXY` unset unless you are following the trusted
TLS reverse-proxy topology in [INSTALL-TLS.md](INSTALL-TLS.md).

Enable the gateway:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now rocguard-web
sudo systemctl status rocguard-web --no-pager
```

Restrict TCP port `8080` to the intended client network. Open
`http://<gateway-host>:8080`, sign in, and immediately change the bootstrap
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
administrator-created accounts only. In this plaintext topology, registration
passwords and the resulting session cookies cross the network without
encryption.

## 4. Register the plaintext nodes

Read a node's root key as an administrator on that node:

```bash
sudo cat /var/lib/rocguard/root.key
```

In the gateway's `Nodes` tab, add:

```text
Name: a display name
Endpoint API: http://<node-host>:8192
Root key: contents of /var/lib/rocguard/root.key on that node
Skip TLS verify: disabled
```

`Skip TLS verify` is irrelevant to HTTP and the UI disables it for plaintext
endpoints. If the gateway reports that plaintext nodes are disabled, confirm
that `ROCGUARD_WEB_ALLOW_INSECURE_NODES=1` is set on the gateway service—not
only on the node.

## 5. Protect and back up gateway state

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

## Plaintext deployment checklist

- Never expose ports `8080` or `8192` to the public Internet.
- Permit node port `8192` only from the gateway host.
- Permit gateway port `8080` only from intended client networks.
- Use a separate root key for every node.
- Understand that passwords, cookies, root keys, and control traffic are not
  confidential on the network.
- Migrate to [INSTALL-TLS.md](INSTALL-TLS.md) before using an untrusted network.

After installation, see [User-Guide.md](User-Guide.md) for normal user
workflows and [README.md](README.md) for upgrades, configuration, and command
reference.
