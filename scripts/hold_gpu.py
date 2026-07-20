#!/usr/bin/env python3
import argparse
import os
import signal
import subprocess
import sys
import time


def install_fast_exit_handlers():
    def handle_signal(signum, _frame):
        print(f"received signal {signum}; exiting immediately", file=sys.stderr, flush=True)
        os._exit(128 + signum)

    signal.signal(signal.SIGINT, handle_signal)
    signal.signal(signal.SIGTERM, handle_signal)


def parse_args():
    parser = argparse.ArgumentParser(description="Hold one or more AMD/ROCm GPUs with PyTorch.")
    parser.add_argument(
        "--gpus",
        dest="gpu_values",
        action="append",
        help="Comma-separated host GPU indices to expose, for example: 2 or 2,3,4.",
    )
    parser.add_argument("--mem-mb", type=int, default=1024, help="Approximate VRAM to allocate per GPU.")
    parser.add_argument("--duration", type=int, default=0, help="Seconds to run; 0 means forever.")
    parser.add_argument("--matrix", type=int, default=2048, help="Square matrix size for compute loop.")
    parser.add_argument("--sleep", type=float, default=0.0, help="Seconds to sleep between compute iterations.")
    parser.add_argument("--children", type=int, default=0, help="Spawn this many child holder processes; 0 runs in this process.")
    args = parser.parse_args()
    args.gpus = parse_gpus(args.gpu_values)
    if args.mem_mb <= 0:
        raise SystemExit("--mem-mb must be positive")
    if args.matrix <= 0:
        raise SystemExit("--matrix must be positive")
    if args.sleep < 0:
        raise SystemExit("--sleep must be >= 0")
    if args.duration < 0:
        raise SystemExit("--duration must be >= 0")
    if args.children < 0 or args.children > 64:
        raise SystemExit("--children must be between 0 and 64")
    return args


def parse_gpus(values):
    if not values:
        values = ["0"]

    gpus = []
    seen = set()
    for value in values:
        for part in value.split(","):
            part = part.strip()
            if not part:
                continue
            try:
                gpu = int(part)
            except ValueError:
                raise SystemExit(f"invalid gpu index: {part!r}")
            if gpu < 0:
                raise SystemExit(f"gpu index must be >= 0: {gpu}")
            if gpu in seen:
                raise SystemExit(f"duplicate gpu index: {gpu}")
            seen.add(gpu)
            gpus.append(gpu)

    if not gpus:
        raise SystemExit("at least one gpu index is required")
    return gpus


def main():
    args = parse_args()
    if args.children > 0:
        return spawn_children(args)

    install_fast_exit_handlers()

    # Must be set before importing torch. PyTorch on ROCm still uses torch.cuda.
    visible_gpus = ",".join(str(gpu) for gpu in args.gpus)
    os.environ["HIP_VISIBLE_DEVICES"] = visible_gpus

    try:
        import torch
    except ImportError:
        print("PyTorch is required: pip install torch", file=sys.stderr)
        return 2

    if not torch.cuda.is_available():
        print("No ROCm/CUDA GPU is visible to PyTorch.", file=sys.stderr)
        return 1

    if torch.cuda.device_count() < len(args.gpus):
        print(
            f"requested {len(args.gpus)} GPU(s) via HIP_VISIBLE_DEVICES={visible_gpus}, "
            f"but PyTorch sees {torch.cuda.device_count()} device(s)",
            file=sys.stderr,
        )
        return 1

    chunk_mb = min(64, args.mem_mb)
    element_size = torch.empty((), dtype=torch.float32, device="cuda:0").element_size()

    holders = []
    try:
        for local_idx, host_gpu in enumerate(args.gpus):
            device = torch.device(f"cuda:{local_idx}")
            tensors = []
            remaining_mb = args.mem_mb
            while remaining_mb > 0:
                allocation_mb = min(chunk_mb, remaining_mb)
                elements = allocation_mb * 1024 * 1024 // element_size
                tensors.append(torch.empty(elements, dtype=torch.float32, device=device))
                remaining_mb -= allocation_mb
            torch.cuda.synchronize(device)
            holders.append(
                {
                    "host_gpu": host_gpu,
                    "device": device,
                    "tensors": tensors,
                    "a": torch.randn((args.matrix, args.matrix), device=device),
                    "b": torch.randn((args.matrix, args.matrix), device=device),
                }
            )
            print(f"holding host gpu={host_gpu} as torch device={device}; allocated ~{args.mem_mb} MiB")
    except RuntimeError as err:
        print(f"allocation failed: {err}", file=sys.stderr)
        return 1

    deadline = None if args.duration <= 0 else time.monotonic() + args.duration

    iterations = 0
    with torch.inference_mode():
        while deadline is None or time.monotonic() < deadline:
            for holder in holders:
                holder["a"] = (holder["a"] @ holder["b"]).relu()
            iterations += 1
            if args.sleep:
                time.sleep(args.sleep)
            if iterations % 10 == 0:
                for holder in holders:
                    torch.cuda.synchronize(holder["device"])
                print(f"still holding gpus {visible_gpus}; iterations={iterations}", flush=True)

    for holder in holders:
        torch.cuda.synchronize(holder["device"])
    print(f"released gpus {visible_gpus}; iterations={iterations}")
    return 0


def spawn_children(args):
    script = os.path.abspath(__file__)
    visible_gpus = ",".join(str(gpu) for gpu in args.gpus)
    children = []
    for idx in range(args.children):
        cmd = [
            sys.executable,
            script,
            "--gpus",
            visible_gpus,
            "--mem-mb",
            str(args.mem_mb),
            "--duration",
            str(args.duration),
            "--matrix",
            str(args.matrix),
            "--sleep",
            str(args.sleep),
            "--children",
            "0",
        ]
        print(f"starting child {idx + 1}/{args.children}: {' '.join(cmd)}", flush=True)
        children.append(subprocess.Popen(cmd))

    def stop_children():
        for child in children:
            if child.poll() is None:
                child.terminate()
        deadline = time.monotonic() + 2
        for child in children:
            if child.poll() is not None:
                continue
            timeout = max(0.0, deadline - time.monotonic())
            try:
                child.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                pass
        for child in children:
            if child.poll() is None:
                child.kill()
        for child in children:
            try:
                child.wait(timeout=1)
            except subprocess.TimeoutExpired:
                pass

    def handle_signal(signum, _frame):
        print(f"received signal {signum}; stopping children", file=sys.stderr, flush=True)
        stop_children()
        os._exit(128 + signum)

    signal.signal(signal.SIGINT, handle_signal)
    signal.signal(signal.SIGTERM, handle_signal)

    exit_code = 0
    try:
        for child in children:
            rc = child.wait()
            if rc != 0 and exit_code == 0:
                exit_code = rc
    except KeyboardInterrupt:
        stop_children()
        return 130
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
