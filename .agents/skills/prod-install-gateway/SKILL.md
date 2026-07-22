---
name: prod-install-gateway
description: How to install the GPUardian web gateway in production — build the UI and image, stage TLS certs, write web.env, bootstrap the admin account, start the Compose service on 8443, and register GPU nodes. Use whenever the user asks to install the gateway, set up the web UI, bootstrap admin, add or register nodes, or deploy the gateway — even if they just say "set up GPUardian" or "install the web part".
---

# Install the web gateway in production

Run **once** on the gateway host. Requires root. The gateway is Dockerized,
non-root (UID/GID `65532`), serves HTTPS on `8443`. Read `README.md`
production install before touching this flow or the Compose files.

## 1. Build the UI, binary, and image

See the `dev-build-test` skill. At minimum:

```bash
npm --prefix web/ui ci && npm --prefix web/ui run build
go test ./...
go build -buildvcs=false -o gpuardian ./cmd/gpuardian
sudo docker compose -f compose.web.yml build
```

The UI must be built before the image (the Dockerfile builds it in a `node`
stage, but `dist/` also needs to exist for non-image dev runs).

## 2. Stage and install TLS certificates

The gateway needs its own cert with a SAN matching the gateway's DNS name or
IP (users browse to `https://<gateway>:8443`).

```bash
sudo install -d -o root -g root -m 0755 /etc/gpuardian
sudo install -o root -g root -m 0644 /tmp/gpuardian-install/web.crt /etc/gpuardian/web.crt
sudo install -o root -g 65532 -m 0640 /tmp/gpuardian-install/web.key /etc/gpuardian/web.key
```

`web.key` is group-readable by `65532` (the container's GID) because the
Compose mount makes it readable inside the container. Each `.crt` = server
cert + intermediates.

Install the **gateway CA** in each user's browser/OS trust store so browsers
trust the gateway cert. Keep `Skip TLS verify` **disabled** when registering
prod nodes.

## 3. Create the state dir and web.env

```bash
sudo install -d -o 65532 -g 65532 -m 0700 /var/lib/gpuardian-web
```

Create `/etc/gpuardian/web.env` (mode `0600`) with **only operator options**:

```bash
sudo install -o root -g root -m 0600 /dev/null /etc/gpuardian/web.env
sudo tee /etc/gpuardian/web.env >/dev/null <<EOF
GPUARDIAN_WEB_USER=admin
GPUARDIAN_WEB_PASSWORD=$(openssl rand -hex 32)
GPUARDIAN_WEB_ALLOW_REGISTRATION=1
EOF
sudo cat /etc/gpuardian/web.env   # read the bootstrap password; store it in a password manager
```

`web.env` should contain **only** `GPUARDIAN_WEB_USER`,
`GPUARDIAN_WEB_PASSWORD` (bootstrap only), `GPUARDIAN_WEB_ALLOW_REGISTRATION`,
and optionally `GPUARDIAN_WEB_DB`. All listener/TLS/cookie/state-path/UI
settings are owned by the prod Compose file (`compose.web.yml`).

## 4. Start the gateway

```bash
sudo docker compose -f compose.web.yml up -d
sudo docker compose -f compose.web.yml ps
sudo docker compose -f compose.web.yml logs --tail=100 gateway
```

The Compose file forces `GPUARDIAN_WEB_ALLOW_INSECURE=0` and
`GPUARDIAN_WEB_ALLOW_INSECURE_NODES=0`, mounts `/var/lib/gpuardian-web`,
`web.crt`/`web.key` (ro), and the host `/etc/ssl/certs/ca-certificates.crt`
(ro) so private node CAs are trusted inside. It's `read_only: true` with
`cap_drop: [ALL]` and `no-new-privileges`.

## 5. Bootstrap admin and remove the bootstrap password

1. Open `https://<gateway>:8443`, sign in as `admin` with the bootstrap
   password from `web.env`.
2. **Change the admin password** immediately.
3. Remove the bootstrap password from `web.env` and recreate the container:

```bash
sudo sed -i '/^GPUARDIAN_WEB_PASSWORD=/d' /etc/gpuardian/web.env
sudo docker compose -f compose.web.yml up -d --force-recreate
```

The gateway seeds the first admin account **only if no users exist yet**
(`BootstrapAdmin` at startup, using `GPUARDIAN_WEB_USER`/`WEB_PASSWORD`).
After the first sign-in the password lives in `users.json` (PBKDF2-hashed);
the `web.env` password is only for bootstrap.

## 6. Register every GPU node

On each node, read its root key (treat as admin secret):

```bash
sudo cat /var/lib/gpuardian/root.key
```

In the gateway UI → `Nodes` tab → `Add node`:

- **Name**: a label for the node.
- **Endpoint**: `https://<node-dns>:8192` (the node's API, **not** the
  gateway port `8443`).
- **Root key**: the `rk_...` from that node.
- **Skip TLS verify**: **disabled** for prod (the node CA must be trusted on
  the gateway host — see the `prod-install-node` skill).

**Do not enter** `0.0.0.0`, `127.0.0.1`, or the gateway port `8443` as the
endpoint. Inside the container those refer to the container itself.

### Registration troubleshooting

- `connection refused` — daemon down, wrong port, or firewall blocking 8192.
- `unknown authority` — install the node-issuing CA on the gateway host and
  restart the container.
- certificate hostname error — the node cert's SAN doesn't match the
  endpoint hostname.
- `401`/`403` — wrong root key for that node.

## 7. Firewall

Allow **TCP 8443 only from the intended networks** (the users who need to
reserve GPUs). Do not expose it broadly.

## Gotchas

- **`web.env` is operator-only.** Don't put listener/TLS/cookie/path settings
  there — those are owned by `compose.web.yml`. Mixing them in causes
  confusion and the Compose file's forced values may override you.
- **The bootstrap password is one-time.** Remove it from `web.env` after the
  first admin signs in and changes their password.
- **Dev and prod share `/var/lib/gpuardian-web` as the in-container bind
  mount path**, but dev points the host side at `.dev/web`. Don't confuse the
  two — prod state lives at `/var/lib/gpuardian-web` on the host.
- **The gateway image is `read_only: true` with `cap_drop: [ALL]`.** Anything
  new that needs writable storage must use the existing tmpfs `/tmp` or the
  `/var/lib/gpuardian-web` volume — don't add filesystem writes elsewhere
  without updating the Compose files.

## Read before sensitive edits

- `README.md` — gateway install, env-var list, node registration.
- `compose.web.yml` — the forced env values, mounts, and hardening.
- `AGENTS.md` — "Gateway image is `read_only: true`" and dev/prod path
  gotchas.
