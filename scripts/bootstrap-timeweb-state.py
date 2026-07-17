#!/usr/bin/env python3
"""Enable and verify versioning for the platform state bucket.

The active platform state path is the HTTP backend.  This helper intentionally
does not materialize a long-lived S3 credentials file or S3 backend configs.
"""

import argparse
from pathlib import Path
import subprocess

import boto3
from botocore.config import Config
from timeweb_s3_credentials import read_s3_pair

ROOT = Path(__file__).resolve().parents[1]
BOOTSTRAP = ROOT / "infra" / "bootstrap" / "timeweb-state"
def output_values():
    completed = subprocess.run(
        ["tofu", f"-chdir={BOOTSTRAP}", "output", "-json"],
        check=True,
        capture_output=True,
        text=True,
    )
    values = json.loads(completed.stdout)
    return {name: item["value"] for name, item in values.items()}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--skip-versioning", action="store_true")
    args = parser.parse_args()
    values = output_values()
    required = {"bucket_name", "endpoint"}
    if missing := required - values.keys():
        raise SystemExit("bootstrap outputs missing: " + ", ".join(sorted(missing)))

    if not args.skip_versioning:
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
        client.put_bucket_versioning(
            Bucket=values["bucket_name"],
            VersioningConfiguration={"Status": "Enabled"},
        )
        status = client.get_bucket_versioning(Bucket=values["bucket_name"]).get("Status")
        if status != "Enabled":
            raise SystemExit(f"Timeweb S3 versioning is {status!r}, expected 'Enabled'")

    versioning = "left unchanged" if args.skip_versioning else "enabled"
    print(f"Timeweb state bucket verified: versioning {versioning}; no credentials were materialized")


if __name__ == "__main__":
    main()
