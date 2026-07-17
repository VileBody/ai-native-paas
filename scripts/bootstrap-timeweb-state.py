#!/usr/bin/env python3
"""Enable Timeweb S3 versioning and materialize ignored backend config safely."""

import argparse
import json
import os
from pathlib import Path
import stat
import subprocess

import boto3
from botocore.config import Config

ROOT = Path(__file__).resolve().parents[1]
BOOTSTRAP = ROOT / "infra" / "bootstrap" / "timeweb-state"
BACKENDS = ROOT / "infra" / "backend"
LOCAL = ROOT / ".state-backend"
STACKS = ("admin", "network-foundation", "cozystack-lab", "workspace-images")


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
    required = {"bucket_name", "endpoint", "access_key", "secret_key"}
    if missing := required - values.keys():
        raise SystemExit("bootstrap outputs missing: " + ", ".join(sorted(missing)))

    if not args.skip_versioning:
        client = boto3.client(
            "s3",
            endpoint_url=values["endpoint"],
            aws_access_key_id=values["access_key"],
            aws_secret_access_key=values["secret_key"],
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

    LOCAL.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chmod(LOCAL, stat.S_IRWXU)
    credentials = LOCAL / "credentials.env"
    credentials.write_text(
        "export AWS_ACCESS_KEY_ID=" + json.dumps(values["access_key"]) + "\n"
        "export AWS_SECRET_ACCESS_KEY=" + json.dumps(values["secret_key"]) + "\n"
        "export AWS_REGION=\"ru-1\"\n"
        "export AWS_ENDPOINT_URL_S3=" + json.dumps(values["endpoint"]) + "\n"
    )
    os.chmod(credentials, stat.S_IRUSR | stat.S_IWUSR)

    for stack in STACKS:
        template = (BACKENDS / f"{stack}.s3.tfbackend.example").read_text()
        destination = LOCAL / f"{stack}.s3.tfbackend"
        destination.write_text(template.replace("TIMEWEB_GENERATED_BUCKET_NAME", values["bucket_name"]))
        os.chmod(destination, stat.S_IRUSR | stat.S_IWUSR)

    versioning = "left unchanged" if args.skip_versioning else "enabled"
    print(f"Timeweb state backend configured: versioning {versioning}; ignored credentials and backend files written")


if __name__ == "__main__":
    main()
