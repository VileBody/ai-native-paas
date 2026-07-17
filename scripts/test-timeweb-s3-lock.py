#!/usr/bin/env python3
"""Prove Timeweb S3 supports native OpenTofu conditional locking and versioning."""

from concurrent.futures import ThreadPoolExecutor
import json
from pathlib import Path
import subprocess
import uuid

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
from timeweb_s3_credentials import read_s3_pair

ROOT = Path(__file__).resolve().parents[1]
BOOTSTRAP = ROOT / "infra" / "bootstrap" / "timeweb-state"


def outputs():
    completed = subprocess.run(
        ["tofu", f"-chdir={BOOTSTRAP}", "output", "-json"],
        check=True,
        capture_output=True,
        text=True,
    )
    return {name: item["value"] for name, item in json.loads(completed.stdout).items()}


def main():
    values = outputs()
    access_key, secret_key = read_s3_pair(
        "STATE_S3_ACCESS_KEY_FILE", "STATE_S3_SECRET_KEY_FILE"
    )
    client = boto3.client(
        "s3",
        endpoint_url=values["endpoint"],
        aws_access_key_id=access_key,
        aws_secret_access_key=secret_key,
        region_name="ru-1",
        config=Config(signature_version="s3v4", s3={"addressing_style": "path"}),
    )
    bucket = values["bucket_name"]
    if client.get_bucket_versioning(Bucket=bucket).get("Status") != "Enabled":
        raise SystemExit("STATE_LOCK_UNSUPPORTED: bucket versioning is not enabled")

    key = f"platform/lock-probe/{uuid.uuid4()}.tflock"

    def contender(index):
        try:
            client.put_object(Bucket=bucket, Key=key, Body=f"contender-{index}".encode(), IfNoneMatch="*")
            return "winner"
        except ClientError as error:
            status = error.response.get("ResponseMetadata", {}).get("HTTPStatusCode")
            code = error.response.get("Error", {}).get("Code")
            if status in (409, 412) or code in ("ConditionalRequestConflict", "PreconditionFailed"):
                return "locked"
            raise

    try:
        with ThreadPoolExecutor(max_workers=16) as pool:
            results = list(pool.map(contender, range(16)))
        winners = results.count("winner")
        locked = results.count("locked")
        if winners != 1 or locked != 15:
            raise SystemExit(f"STATE_LOCK_UNSUPPORTED: winners={winners}, locked={locked}")

        client.put_object(Bucket=bucket, Key=key, Body=b"version-two")
        versions = client.list_object_versions(Bucket=bucket, Prefix=key)
        exact_versions = [item for item in versions.get("Versions", []) if item.get("Key") == key]
        if len(exact_versions) < 2:
            raise SystemExit(f"STATE_VERSIONING_UNSUPPORTED: versions={len(exact_versions)}")
    finally:
        versions = client.list_object_versions(Bucket=bucket, Prefix=key)
        objects = [
            {"Key": item["Key"], "VersionId": item["VersionId"]}
            for category in ("Versions", "DeleteMarkers")
            for item in versions.get(category, [])
            if item.get("Key") == key
        ]
        if objects:
            client.delete_objects(Bucket=bucket, Delete={"Objects": objects, "Quiet": True})

    print("STATE_LOCK_SUPPORTED: 1 winner, 15 conditional conflicts, object versioning verified")


if __name__ == "__main__":
    main()
