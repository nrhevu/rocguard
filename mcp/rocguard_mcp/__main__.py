"""Entry point for the RocGuard MCP server (stdio transport).

Configuration via environment variables:

  ROCGUARD_MCP_URL       Web gateway URL (required)
  ROCGUARD_MCP_USER      Username (required)
  ROCGUARD_MCP_PASSWORD  Password (required)
  ROCGUARD_MCP_TIMEOUT   HTTP timeout in seconds (default 30)
  ROCGUARD_MCP_VERIFY_TLS  Verify TLS certificates, 1/0 (default 1)
"""

from __future__ import annotations

import os
import sys

from mcp.server.stdio import stdio_server

from .client import RocGuardClient, RocGuardError
from .server import create_server


def _env(name: str, default: str | None = None) -> str:
    value = os.environ.get(name)
    if value is None or value == "":
        if default is None:
            print(f"rocguard-mcp: missing required env var {name}", file=sys.stderr)
            sys.exit(1)
        return default
    return value


def main() -> None:
    base_url = _env("ROCGUARD_MCP_URL")
    username = _env("ROCGUARD_MCP_USER")
    password = _env("ROCGUARD_MCP_PASSWORD")
    timeout = float(_env("ROCGUARD_MCP_TIMEOUT", "30"))
    verify_tls = _env("ROCGUARD_MCP_VERIFY_TLS", "1") != "0"

    client = RocGuardClient(
        base_url=base_url,
        username=username,
        password=password,
        timeout=timeout,
        verify_tls=verify_tls,
    )

    # Eagerly login so config errors surface before the MCP loop starts.
    try:
        client.login()
    except RocGuardError as exc:
        print(f"rocguard-mcp: login failed: {exc}", file=sys.stderr)
        sys.exit(1)

    app = create_server(client)

    import asyncio

    async def run() -> None:
        async with stdio_server() as (read_stream, write_stream):
            await app.run(read_stream, write_stream, app.create_initialization_options())

    try:
        asyncio.run(run())
    finally:
        client.close()


if __name__ == "__main__":
    main()
