#!/usr/bin/env python3
"""Materialize ignored OpenTofu HTTP backend files without credentials."""

import argparse
from pathlib import Path
import os
import stat
from urllib.parse import urlparse

ROOT = Path(__file__).resolve().parents[1]
LOCAL = ROOT / ".state-backend"
NAMESPACES = ("admin", "cozystack-lab", "workspace-images", "state-bootstrap")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", required=True)
    args = parser.parse_args()
    base_url = args.base_url.rstrip("/")
    parsed = urlparse(base_url)
    if parsed.scheme not in ("http", "https") or not parsed.netloc:
        raise SystemExit("base URL must be an absolute HTTP(S) URL")
    if parsed.scheme != "https" and parsed.hostname not in ("127.0.0.1", "localhost", "::1"):
        raise SystemExit("plaintext HTTP is allowed only for a local loopback endpoint")

    LOCAL.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chmod(LOCAL, stat.S_IRWXU)
    for namespace in NAMESPACES:
        address = f"{base_url}/api/v1/state/{namespace}"
        destination = LOCAL / f"{namespace}.http.tfbackend"
        destination.write_text(
            f'address        = "{address}"\n'
            f'lock_address   = "{address}/lock"\n'
            f'unlock_address = "{address}/lock"\n'
            'lock_method    = "LOCK"\n'
            'unlock_method  = "UNLOCK"\n'
            'retry_wait_min = 1\n'
            'retry_wait_max = 10\n'
        )
        os.chmod(destination, stat.S_IRUSR | stat.S_IWUSR)

    print("HTTP state backend files written; credentials remain environment-only")


if __name__ == "__main__":
    main()
