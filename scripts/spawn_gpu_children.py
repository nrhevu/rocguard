#!/usr/bin/env python3
import argparse
import os
import subprocess
import sys


def parse_args():
    parser = argparse.ArgumentParser(description="Spawn child GPU holder processes for Rocguard cgroup tests.")
    parser.add_argument("--gpus", required=True, help="Comma-separated host GPU indices for children, for example: 2 or 2,3.")
    parser.add_argument("--children", type=int, default=2, help="Number of child processes to spawn.")
    parser.add_argument("--mem-mb", type=int, default=64, help="Approximate VRAM to allocate per child per GPU.")
    parser.add_argument("--duration", type=int, default=10, help="Seconds each child should run.")
    parser.add_argument("--matrix", type=int, default=128, help="Square matrix size for child compute loop.")
    parser.add_argument("--sleep", type=float, default=0.2, help="Seconds to sleep between child compute iterations.")
    args = parser.parse_args()
    if args.children <= 0:
        raise SystemExit("--children must be positive")
    return args


def main():
    args = parse_args()
    here = os.path.dirname(os.path.abspath(__file__))
    hold_gpu = os.path.join(here, "hold_gpu.py")
    children = []
    for idx in range(args.children):
        cmd = [
            sys.executable,
            hold_gpu,
            "--gpus",
            args.gpus,
            "--mem-mb",
            str(args.mem_mb),
            "--duration",
            str(args.duration),
            "--matrix",
            str(args.matrix),
            "--sleep",
            str(args.sleep),
        ]
        print(f"starting child {idx + 1}/{args.children}: {' '.join(cmd)}", flush=True)
        children.append(subprocess.Popen(cmd))

    exit_code = 0
    for child in children:
        rc = child.wait()
        if rc != 0 and exit_code == 0:
            exit_code = rc
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
