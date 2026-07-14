#!/usr/bin/env python3
"""Exercise the HTTP backend lock protocol against a live state service."""

from concurrent.futures import ThreadPoolExecutor
import base64
import json
import os
import urllib.error
import urllib.request
import uuid


def main():
    base_url = os.environ.get("STATE_HTTP_URL", "").rstrip("/")
    username = os.environ.get("TF_HTTP_USERNAME", "")
    password = os.environ.get("TF_HTTP_PASSWORD", "")
    if not base_url or not username or not password:
        raise SystemExit("STATE_HTTP_URL, TF_HTTP_USERNAME and TF_HTTP_PASSWORD are required")
    auth = base64.b64encode(f"{username}:{password}".encode()).decode()

    def call(method, body):
        request = urllib.request.Request(
            base_url + "/lock",
            data=json.dumps(body).encode(),
            method=method,
            headers={"Authorization": "Basic " + auth, "Content-Type": "application/json"},
        )
        try:
            with urllib.request.urlopen(request, timeout=15) as response:
                return response.status
        except urllib.error.HTTPError as error:
            return error.code

    contenders = [str(uuid.uuid4()) for _ in range(16)]
    with ThreadPoolExecutor(max_workers=16) as pool:
        statuses = list(pool.map(lambda lock_id: call("LOCK", {"ID": lock_id, "Who": "live-gate"}), contenders))
    winners = [contenders[index] for index, status in enumerate(statuses) if status == 200]
    conflicts = statuses.count(423)
    if len(winners) != 1 or conflicts != 15:
        raise SystemExit(f"HTTP_STATE_LOCK_FAILED: winners={len(winners)}, conflicts={conflicts}, statuses={statuses}")
    unlock_status = call("UNLOCK", {"ID": winners[0]})
    if unlock_status != 200:
        raise SystemExit(f"HTTP_STATE_UNLOCK_FAILED: status={unlock_status}")
    print("HTTP_STATE_LOCK_SUPPORTED: 1 winner, 15 conflicts, exact-ID unlock verified")


if __name__ == "__main__":
    main()
