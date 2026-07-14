#!/usr/bin/env python3
"""Enable and verify versioning on the dedicated encrypted workspace log bucket."""

import json
from pathlib import Path
import subprocess

import boto3
from botocore.config import Config

ROOT = Path(__file__).resolve().parents[1]
ADMIN = ROOT / "infra" / "stacks" / "admin"


def main():
    completed = subprocess.run(
        ["tofu", f"-chdir={ADMIN}", "output", "-json"],
        check=True,
        capture_output=True,
        text=True,
    )
    outputs = json.loads(completed.stdout)
    values = {name: item["value"] for name, item in outputs.items()}
    required = {
        "workspace_log_bucket_name",
        "workspace_log_s3_endpoint",
        "workspace_log_s3_access_key",
        "workspace_log_s3_secret_key",
    }
    if missing := required - values.keys():
        raise SystemExit("workspace log outputs missing: " + ", ".join(sorted(missing)))

    client = boto3.client(
        "s3",
        endpoint_url=values["workspace_log_s3_endpoint"],
        aws_access_key_id=values["workspace_log_s3_access_key"],
        aws_secret_access_key=values["workspace_log_s3_secret_key"],
        region_name="ru-1",
        config=Config(signature_version="s3v4", s3={"addressing_style": "path"}),
    )
    bucket = values["workspace_log_bucket_name"]
    client.put_bucket_versioning(Bucket=bucket, VersioningConfiguration={"Status": "Enabled"})
    status = client.get_bucket_versioning(Bucket=bucket).get("Status")
    if status != "Enabled":
        raise SystemExit(f"workspace log bucket versioning is {status!r}, expected 'Enabled'")
    print(f"workspace log bucket versioning enabled: {bucket}")


if __name__ == "__main__":
    main()
