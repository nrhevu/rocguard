
## User Guide

Use Rocguard when you need to run workloads on shared GPUs. The usual flow is:
reserve GPU time, copy the returned key, then run or authorize your workload
with that key.

### Create an account

If the sign-in page shows `Create account`, select it, choose a username, and
enter the same password twice. Passwords must contain at least 12 bytes. A new
account is a regular user account and is signed in immediately after creation.

If `Create account` is not shown, self-registration is disabled and an
administrator must create your account from the gateway's `Users` tab.

### Sign in

Open the Rocguard gateway URL from your admin and sign in with your Rocguard
username and password.

### Check GPUs

1. Choose a node from the left sidebar.
2. Open the `Schedule` tab.
3. Look at each GPU card:
   - `Available` means it can be reserved.
   - `Reserved` means it already has a scheduled reservation.
   - `Claimed` means it is currently claimed by a running job.
4. Memory and utilization show the current GPU load.

### Reserved vs claimed keys

Rocguard has two key modes:

- `Reserved` keys are for scheduled GPU time. Pick the GPU, start time, and end
  time first, then use the returned key during that window.
- `Claimed` keys are admin-created keys for flexible use. The key is not tied to
  a schedule. When an authorized process starts using a GPU, Rocguard claims
  that GPU for the key. Other users cannot use that GPU until the claim is
  gone.

Use `Reserved` when you know the exact time and GPUs you need. Ask an admin for
a `Claimed` key only when a long-lived key is appropriate for a less scheduled
workflow.

### Reserve GPU time

1. Select one or more available GPUs.
2. Pick the start and end time.
3. Enter the `Purpose`. The reservation owner is your signed-in account.
4. Click `Submit`.
5. In the reservation details, click `Show key`, then click `Copy`. The secret
   is hidden again after you close the key dialog.

If the selected time overlaps another reservation, or a selected GPU already has
a running process, Rocguard rejects the reservation and shows a short error.

### Run a command with `rocguard run`

For a normal shell command, run it through the Rocguard wrapper:

```bash
KEY=rg_xxx rocguard run -- python train.py
```

Everything after `--` is your command. Rocguard authorizes that command while it
runs, including child processes. Use the key returned by the reservation. A
reserved key only works during its reserved time window.

More examples:

```bash
KEY=rg_xxx rocguard run -- bash train.sh
KEY=rg_xxx rocguard run -- torchrun --nproc_per_node=8 train.py
```

### Authorize an existing scope with `rocguard allow`

Use `rocguard allow` when the workload is started by another system and cannot
be wrapped directly with `rocguard run`.

Authorize a Docker container:

```bash
KEY=rg_xxx rocguard allow docker --container trainer
```

Authorize a Kubernetes namespace:

```bash
KEY=rg_xxx rocguard allow k8s --namespace training
```

Authorize all processes from one Linux user:

```bash
KEY=rg_xxx rocguard allow user --name alice
```

Use exact values and keep allow rules as narrow as possible. Wildcard values,
such as `training-*` or `codex*`, can authorize a much broader scope and are
therefore available only to admins in the gateway. For direct local CLI
requests over the Rocguard Unix socket, the caller must also be root to create
a wildcard scope; regular users can create exact scopes only.

### View or revoke keys

Open the `Key` tab to see keys and reservations without secrets.

- `Show key` reveals a key you own. Admins can reveal any stored key.
- `Revoke` removes a key or reservation you own. Admins can revoke any item.

Normal users should never need or receive a node root key. Ask an admin if a key
must be shown or revoked and you do not have permission.

### Check status from the CLI

```bash
KEY=rg_xxx rocguard status
KEY=rg_xxx rocguard ps
KEY=rg_xxx rocguard token info
```

### Simple rules

- Reserve before running shared GPUs.
- Keep reservations short and accurate.
- Revoke reservations you no longer need.
- Do not share your returned key with other users.
- If your job is killed, check whether it was outside its reservation window or
  running on the wrong GPU.
