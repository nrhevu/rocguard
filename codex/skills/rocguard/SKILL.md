---
name: rocguard
description: Protect or yield a user's shared AMD GPU development session with RocGuard. Use when the user tags RocGuard or asks to protect, authorize, allow, claim, share, yield, release, or hand off GPU usage with a RocGuard key, especially for Docker containers, Kubernetes namespaces, Linux users, or bare shell commands.
---

# RocGuard

Use this skill to make RocGuard low-friction for the user. The ideal user flow is:
they paste or provide a RocGuard key, tag this skill, and Codex chooses the
safe default authorization needed to protect or yield the GPU session.

## Operating Rules

- Treat the RocGuard key as a secret. Do not repeat it in responses, logs, or
  summaries. Redact it as `rg_...`.
- The RocGuard CLI reads the secret from `KEY`, not `ROCGUARD_KEY`.
- Always run `KEY=... rocguard token info` before creating or changing rules.
- If the user does not provide a scope, default to current Linux user
  authorization with `rocguard allow user --name "$(id -un)"`.
- Use Docker or Kubernetes scope only when the user provides it or asks for
  narrower scope detection.
- Do not create wildcard rules for regular users. Treat wildcard scopes as
  admin-only and require explicit admin intent before using a user-supplied
  wildcard pattern.
- Do not revoke scheduler reservations or tokens during yield handoffs unless
  the user explicitly asks for that destructive action.
- Only revoke authorization IDs when the user explicitly provides/admin-authorizes
  a root key. Never guess whether an ID is safe to revoke without checking
  `token info`.
- Do not stop or kill workloads just to yield GPU access unless the user
  explicitly asks and the exact target process/container is identified.

## Default Flow

1. Identify intent:
   - **Protect**: "protect", "claim", "authorize", "allow", "guard", or a key
     with no handoff wording.
   - **Yield**: "yield", "release", "share", "handoff", "nhuong", "nhường",
     "cho người khác dùng", or similar handoff wording.
2. Get the key from the user message or the user's shell variable. If the user
   provided `ROCGUARD_KEY`, map it as `KEY="$ROCGUARD_KEY"`.
3. Inspect key metadata:

```bash
KEY=rg_xxx rocguard token info
rocguard ps
```

4. Read token mode, reservation window, GPUs, existing authorizations, and
   whether active rules already cover the intended scope.
5. If no scope is supplied, use current Linux user as the default scope.
6. Choose the safe action and execute it.
7. Verify with `KEY=... rocguard token info` and report only non-secret
   metadata: mode, scope, authorization ID, GPUs/window, and any remaining
   active rules.

## Protect Workflow

Use this when the user wants their current development session protected.

1. If the user gives an explicit scope, use it:

```bash
KEY=rg_xxx rocguard allow docker --container <container-name-or-id>
KEY=rg_xxx rocguard allow k8s --namespace <namespace>
KEY=rg_xxx rocguard allow user --name <linux-user>
```

2. If no scope is supplied, default to the current Linux user:

```bash
id -un
KEY=rg_xxx rocguard allow user --name "$(id -un)"
```

3. If the user asks for narrower scope detection, use read-only probes:

```bash
id -un
hostname
test -f /.dockerenv && echo docker
cat /proc/self/cgroup
test -f /var/run/secrets/kubernetes.io/serviceaccount/namespace && cat /var/run/secrets/kubernetes.io/serviceaccount/namespace
docker ps --no-trunc --format '{{.ID}}\t{{.Names}}' 2>/dev/null
```

4. Pick a narrower rule only when the user supplied or requested it:
   - If a Docker container name or ID is supplied/resolvable, run
     `rocguard allow docker --container ...`.
   - Else if a Kubernetes namespace is supplied/detected, run
     `rocguard allow k8s --namespace ...`.
   - Else keep the default Linux user rule.
5. For a bare command that Codex is actually launching, prefer:

```bash
KEY=rg_xxx rocguard run -- <command>
```

Do not use `rocguard run -- docker run ...` for Docker workloads. Docker places
the real workload in a separate container cgroup; protect Docker with
`rocguard allow docker`.

## Yield Workflow

Use this when the user wants to let someone else use the GPU without revoking
the scheduled reservation.

1. Inspect the key:

```bash
KEY=rg_xxx rocguard token info
rocguard ps
```

2. Identify the recipient scope. If the user did not provide it, ask only for
   the missing target: Linux username, Docker container name/ID, or Kubernetes
   namespace.
3. Add the recipient authorization with the same key:

```bash
KEY=rg_xxx rocguard allow user --name <other-linux-user>
KEY=rg_xxx rocguard allow docker --container <other-container>
KEY=rg_xxx rocguard allow k8s --namespace <other-namespace>
```

4. Do not revoke the reservation/token. If the current user's old allow rule
   should be removed, revoke only that authorization ID and only with explicit
   root-key/admin authorization:

```bash
ROOT_KEY=rk_xxx rocguard revoke <authorization-id>
```

5. If no root key is available, tell the user the new recipient rule was added
   but the old authorization still exists. Ask them to stop their workload or
   ask an admin to revoke the specific authorization ID if they want a clean
   handoff.

## Scope Detection Notes

- Docker rules may use exact container names or IDs. Treat wildcard container
  patterns as admin-only.
- Kubernetes rules authorize a namespace, not a single pod. Avoid using a shared
  namespace unless the user explicitly wants that scope.
- Linux user rules authorize all matching processes from that user. This is the
  default for low-friction Codex dev sessions where container names are unknown.
- Reserved keys work only inside the reservation window. Claimed keys claim a
  GPU when an authorized process starts using GPU memory.
- If a claimed GPU is already busy with another workload before the claim
  starts, RocGuard rejects the new claimed process and leaves the existing
  workload alone.

## Response Pattern

Keep responses short:

- Say whether the action was protect or yield.
- Report `mode`, `scope`, `authorization_id`, and GPU/window metadata.
- Mention any remaining active authorizations and whether they were left alone.
- Never include the raw Rocguard key.
