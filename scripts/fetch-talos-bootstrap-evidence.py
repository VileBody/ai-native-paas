#!/usr/bin/env python3
"""Fetch, decrypt and verify a private Talos bootstrap result bundle."""

import argparse
import hashlib
import json
import os
import stat
import subprocess
import tarfile
import tempfile
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


def write_private(path: Path, data: bytes):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "wb") as output:
        output.write(data)
    os.chmod(path, stat.S_IRUSR | stat.S_IWUSR)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--stage", type=Path, required=True)
    parser.add_argument("--passphrase-file", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--delete-staged-artifacts", action="store_true")
    args = parser.parse_args()

    stage = json.loads(args.stage.read_text(encoding="utf-8"))
    if stage.get("schema") != "ai-native-paas.io/talos-bootstrap-stage/v1":
        raise SystemExit("unexpected Talos bootstrap stage schema")
    passphrase = args.passphrase_file.read_bytes().strip()
    if len(passphrase) < 32:
        raise SystemExit("bootstrap passphrase is too short")

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
    encrypted = client.get_object(
        Bucket=stage["bucket"], Key=stage["evidence_key"]
    )["Body"].read()
    if not encrypted:
        raise RuntimeError("Talos bootstrap evidence object is empty")

    with tempfile.TemporaryDirectory(prefix="paas-talos-evidence-") as temporary:
        temporary_path = Path(temporary)
        encrypted_path = temporary_path / "result.enc"
        archive_path = temporary_path / "result.tar.gz"
        write_private(encrypted_path, encrypted)
        subprocess.run(
            [
                "openssl",
                "enc",
                "-d",
                "-aes-256-cbc",
                "-pbkdf2",
                "-iter",
                "600000",
                "-md",
                "sha256",
                "-pass",
                "stdin",
                "-in",
                str(encrypted_path),
                "-out",
                str(archive_path),
            ],
            input=passphrase + b"\n",
            check=True,
        )
        with tarfile.open(archive_path, "r:gz") as archive:
            members = {member.name: member for member in archive.getmembers()}
            required = {"evidence.json", "kubeconfig", "talosconfig"}
            if set(members) != required:
                raise RuntimeError(f"unexpected evidence archive members: {sorted(members)}")
            for member in members.values():
                if not member.isfile() or member.name.startswith("/") or ".." in Path(member.name).parts:
                    raise RuntimeError("unsafe evidence archive member")
            files = {
                name: archive.extractfile(members[name]).read() for name in sorted(required)
            }

    evidence = json.loads(files["evidence.json"])
    expected_nodes = ["192.168.74.11", "192.168.74.12", "192.168.74.13"]
    if evidence.get("schema") != "ai-native-paas.io/talos-bootstrap-evidence/v1":
        raise RuntimeError("unexpected Talos bootstrap evidence schema")
    if evidence.get("generation") != stage["generation"] or evidence.get("success") is not True:
        raise RuntimeError("Talos bootstrap evidence generation/success mismatch")
    if evidence.get("nodes") != expected_nodes:
        raise RuntimeError("Talos bootstrap evidence node identities changed")
    if evidence.get("bundle_sha256") != "sha256:" + stage["bundle_sha256"]:
        raise RuntimeError("Talos bootstrap evidence input bundle mismatch")
    kubeconfig_hash = hashlib.sha256(files["kubeconfig"]).hexdigest()
    if evidence.get("kubeconfig_sha256") != "sha256:" + kubeconfig_hash:
        raise RuntimeError("Talos bootstrap evidence kubeconfig digest mismatch")

    args.output_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(args.output_dir, stat.S_IRWXU)
    for name, data in files.items():
        write_private(args.output_dir / name, data)
    write_private(args.output_dir / "result.enc", encrypted)
    if args.delete_staged_artifacts:
        objects = [
            {"Key": stage["bundle_key"]},
            {"Key": stage["evidence_key"]},
        ]
        if stage.get("talosctl_key"):
            objects.append({"Key": stage["talosctl_key"]})
        client.delete_objects(
            Bucket=stage["bucket"],
            Delete={"Objects": objects},
        )
    print(
        f"TALOS_BOOTSTRAP_EVIDENCE=PASS generation={stage['generation']} "
        f"output_dir={args.output_dir}"
    )


if __name__ == "__main__":
    main()
