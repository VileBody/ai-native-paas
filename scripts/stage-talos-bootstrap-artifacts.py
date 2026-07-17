#!/usr/bin/env python3
"""Stage an encrypted Talos bundle and create short-lived runner URLs."""

import argparse
import hashlib
import json
import os
import stat
import subprocess
from datetime import datetime, timedelta, timezone
from pathlib import Path

import boto3
from botocore.config import Config
from timeweb_s3_credentials import read_s3_pair

ROOT = Path(__file__).resolve().parents[1]
STACK = ROOT / "infra" / "stacks" / "workspace-images"


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
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--talosctl", type=Path)
    parser.add_argument("--generation", type=int, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--expires-seconds", type=int, default=3600)
    args = parser.parse_args()
    if args.generation < 1:
        raise SystemExit("generation must be positive")
    if not 900 <= args.expires_seconds <= 21600:
        raise SystemExit("expires-seconds must be between 900 and 21600")
    bundle = args.bundle.read_bytes()
    digest = hashlib.sha256(bundle).hexdigest()
    talosctl = args.talosctl.read_bytes() if args.talosctl else None
    talosctl_digest = hashlib.sha256(talosctl).hexdigest() if talosctl else None
    if talosctl_digest and talosctl_digest != "4aa5cd191c708b8c1a3b358bfd7dd21fb0cc6bd4dc7a07f2aa925cd2a8473bae":
        raise SystemExit("talosctl does not match the reviewed v1.13.0 Linux AMD64 digest")

    values = stack_outputs()
    required = {
        "image_staging_bucket_name",
        "image_staging_endpoint",
    }
    if missing := required - values.keys():
        raise SystemExit("workspace-images outputs missing: " + ", ".join(sorted(missing)))

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
    bucket = values["image_staging_bucket_name"]
    prefix = f"talos-bootstrap/generation-{args.generation}"
    bundle_key = f"{prefix}/bootstrap.enc"
    evidence_key = f"{prefix}/result.enc"
    talosctl_key = f"{prefix}/talosctl-linux-amd64" if talosctl else None
    client.put_object(
        Bucket=bucket,
        Key=bundle_key,
        Body=bundle,
        ContentType="application/octet-stream",
        Metadata={"sha256": digest, "generation": str(args.generation)},
    )
    staged = client.head_object(Bucket=bucket, Key=bundle_key)
    if staged["ContentLength"] != len(bundle) or staged.get("Metadata", {}).get("sha256") != digest:
        raise RuntimeError("staged bootstrap bundle failed size/digest metadata verification")
    if talosctl is not None:
        client.put_object(
            Bucket=bucket,
            Key=talosctl_key,
            Body=talosctl,
            ContentType="application/octet-stream",
            Metadata={"sha256": talosctl_digest, "generation": str(args.generation)},
        )
        staged_tool = client.head_object(Bucket=bucket, Key=talosctl_key)
        if staged_tool["ContentLength"] != len(talosctl) or staged_tool.get("Metadata", {}).get("sha256") != talosctl_digest:
            raise RuntimeError("staged talosctl failed size/digest metadata verification")

    bundle_url = client.generate_presigned_url(
        "get_object",
        Params={"Bucket": bucket, "Key": bundle_key},
        ExpiresIn=args.expires_seconds,
    )
    evidence_upload_url = client.generate_presigned_url(
        "put_object",
        Params={"Bucket": bucket, "Key": evidence_key},
        ExpiresIn=args.expires_seconds,
    )
    talosctl_url = (
        client.generate_presigned_url(
            "get_object",
            Params={"Bucket": bucket, "Key": talosctl_key},
            ExpiresIn=args.expires_seconds,
        )
        if talosctl is not None
        else None
    )
    payload = {
        "schema": "ai-native-paas.io/talos-bootstrap-stage/v1",
        "generation": args.generation,
        "bundle_sha256": digest,
        "bundle_url": bundle_url,
        "evidence_upload_url": evidence_upload_url,
        "bucket": bucket,
        "bundle_key": bundle_key,
        "evidence_key": evidence_key,
        "expires_at": (
            datetime.now(timezone.utc) + timedelta(seconds=args.expires_seconds)
        ).isoformat(),
    }
    if talosctl is not None:
        payload.update(
            {
                "talosctl_key": talosctl_key,
                "talosctl_sha256": talosctl_digest,
                "talosctl_url": talosctl_url,
            }
        )
    args.output.parent.mkdir(parents=True, exist_ok=True)
    descriptor = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as output:
        json.dump(payload, output, sort_keys=True)
        output.write("\n")
    os.chmod(args.output, stat.S_IRUSR | stat.S_IWUSR)
    print(
        f"TALOS_BOOTSTRAP_STAGE=PASS generation={args.generation} "
        f"bundle_sha256={digest} metadata={args.output}"
    )


if __name__ == "__main__":
    main()
