# RocGuard Skill User Guide

Use the `$rocguard` skill when you want Codex to protect, share, or reclaim a
shared GPU session with as little manual work as possible.

## What you provide

At minimum, provide:

```text
$rocguard protect
key: rg_xxx
```

For handoff:

```text
$rocguard yield
key: rg_xxx
to user: alice
```

For reclaim:

```text
$rocguard protect lại
key: rg_xxx
```

You can also target Docker or Kubernetes explicitly:

```text
$rocguard protect
key: rg_xxx
docker: trainer
```

```text
$rocguard yield
key: rg_xxx
to k8s namespace: training
```

## What Codex does

Codex will:

1. Treat the key as a secret and avoid repeating it.
2. Check key metadata with `rocguard token info`.
3. Check current GPU ownership with `rocguard ps`.
4. Default to the current Linux user unless you specify Docker/Kubernetes.
5. Add the needed RocGuard rule.
6. Verify and report non-secret metadata only.

## Protect your own session

Use this when you want your GPU process protected.

```text
$rocguard protect
key: rg_xxx
```

If you know the scope, include it:

```text
$rocguard protect
key: rg_xxx
docker: <container-name-or-id>
```

```text
$rocguard protect
key: rg_xxx
k8s namespace: <namespace>
```

```text
$rocguard protect
key: rg_xxx
linux user: <username>
```

If you do not include a scope, Codex will use the current Linux user:

```bash
KEY=... rocguard allow user --name "$(id -un)"
```

Use Docker or Kubernetes only when you want a narrower rule and know the target.

## Yield GPU to someone else

Use this when you want someone else to use the GPU without revoking your
scheduled reservation.

```text
$rocguard yield
key: rg_xxx
to user: alice
```

Other target forms:

```text
$rocguard yield
key: rg_xxx
to docker: trainer
```

```text
$rocguard yield
key: rg_xxx
to k8s namespace: training
```

Codex will add an authorization rule for the recipient. It will not revoke the
reservation or token. If an old authorization rule should be removed cleanly,
that needs an admin/root key for `rocguard revoke <authorization-id>`.

## Use GPU again after yielding

Use the same protect prompt again:

```text
$rocguard protect lại
key: rg_xxx
```

Codex will check whether your old rule still exists. If it does, it will avoid
creating a duplicate rule. If not, it will add a new narrow rule for your current
session.

## Best low-friction pattern

If you do not want to paste the key into chat, set it in your shell:

```bash
export ROCGUARD_KEY=rg_xxx
```

Then tell Codex:

```text
$rocguard protect
use ROCGUARD_KEY
```

The skill knows RocGuard itself expects `KEY`, so Codex will map it locally as:

```bash
KEY="$ROCGUARD_KEY" rocguard token info
```

## Safety notes

- Do not paste the key into shared logs or public chat.
- Docker workloads should use `rocguard allow docker`, not
  `rocguard run -- docker run ...`.
- Kubernetes authorization is namespace-level, not pod-level.
- Linux user authorization is broad, but it is the default because it works
  across dev sessions when container names are unknown.
- Yielding means adding/changing authorization rules, not deleting the scheduler
  reservation.
