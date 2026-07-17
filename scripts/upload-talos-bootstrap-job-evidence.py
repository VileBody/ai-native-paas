#!/usr/bin/env python3
"""Upload an encrypted bootstrap result emitted by the ephemeral admin Job."""

import argparse
import base64
import json
import re
import subprocess
from pathlib import Path

import boto3
from botocore.config import Config
from timeweb_s3_credentials import read_s3_pair

ROOT = Path(__file__).resolve().parents[1]
STACK = ROOT / "infra" / "stacks" / "workspace-images"
RESULT = re.compile(r"^TALOS_BOOTSTRAP_RESULT_BASE64=([A-Za-z0-9+/=]+)$", re.MULTILINE)


def stack_outputs():
    completed = subprocess.run(
        ["tofu", f"-chdir={STACK}", "output", "-json"],
        check=True,
        capture_output=True,
        text=True,
    )
    outputs = json.loads(completed.stdout)
    return {name: item["value"] for name, item in outputs.items()}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--stage", type=Path, required=True)
    parser.add_argument("--job-log", type=Path, required=True)
    args = parser.parse_args()

    stage = json.loads(args.stage.read_text(encoding="utf-8"))
    if stage.get("schema") != "ai-native-paas.io/talos-bootstrap-stage/v1":
        raise SystemExit("unexpected Talos bootstrap stage schema")
    matches = RESULT.findall(args.job_log.read_text(encoding="utf-8"))
    if len(matches) != 1:
        raise SystemExit(f"expected exactly one encrypted result in Job log, got {len(matches)}")
    encrypted = base64.b64decode(matches[0], validate=True)
    if len(encrypted) < 256 or not encrypted.startswith(b"Salted__"):
        raise SystemExit("Job result is not the expected OpenSSL encrypted payload")

    values = stack_outputs()
    access_key, secret_key = read_s3_pair(
        "IMAGE_STAGING_S3_ACCESS_KEY_FILE", "IMAGE_STAGING_S3_SECRET_KEY_FILE"
    )
    client = boto3.client(
        "s3",
        endpoint_url=values["image_staging_endpoint"],
        aws_access_key_id=access_key,
        aws_secret_access_key=secret_key,
        region_name="ru-1",
        config=Config(signature_version="s3v4", s3={"addressing_style": "path"}),
    )
    client.put_object(
        Bucket=stage["bucket"],
        Key=stage["evidence_key"],
        Body=encrypted,
        ContentType="application/octet-stream",
        Metadata={"generation": str(stage["generation"]), "transport": "admin-kubernetes-job"},
    )
    print(
        f"TALOS_BOOTSTRAP_JOB_UPLOAD=PASS generation={stage['generation']} "
        f"bytes={len(encrypted)}"
    )


if __name__ == "__main__":
    main()
