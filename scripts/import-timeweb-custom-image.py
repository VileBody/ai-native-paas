#!/usr/bin/env python3
"""Stream an xz-compressed RAW image into private S3 and import it in Timeweb.

The decompressed image never touches local disk. The staging object is removed
after Timeweb reports the custom image as created.
"""

import argparse
import hashlib
import json
import lzma
import os
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

import boto3
import certifi
from botocore.config import Config
from timeweb_s3_credentials import read_s3_pair

ROOT = Path(__file__).resolve().parents[1]
STACK = ROOT / "infra" / "stacks" / "workspace-images"
TIMEWEB_API = "https://api.timeweb.cloud/api/v1"
PART_SIZE = 16 * 1024 * 1024
READ_SIZE = 1024 * 1024
SSL_CONTEXT = ssl.create_default_context(cafile=certifi.where())


def stack_outputs():
    completed = subprocess.run(
        ["tofu", f"-chdir={STACK}", "output", "-json"],
        check=True,
        capture_output=True,
        text=True,
    )
    values = json.loads(completed.stdout)
    return {name: item["value"] for name, item in values.items()}


def api_request(token, method, path, payload=None):
    body = None if payload is None else json.dumps(payload).encode()
    request = urllib.request.Request(
        TIMEWEB_API + path,
        data=body,
        method=method,
        headers={
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
            "User-Agent": "ai-native-paas-image-import/1.0",
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=120, context=SSL_CONTEXT) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        response = error.read().decode(errors="replace")
        raise RuntimeError(f"Timeweb API {method} {path} returned {error.code}: {response}") from error


def upload_raw(client, bucket, key, source, expected_compressed_sha, source_file=None):
    compressed_hash = hashlib.sha256()
    if source_file:
        with source_file.open("rb") as compressed:
            while chunk := compressed.read(READ_SIZE):
                compressed_hash.update(chunk)
        actual_compressed_sha = compressed_hash.hexdigest()
        if actual_compressed_sha != expected_compressed_sha:
            raise RuntimeError(
                f"compressed sha256 mismatch: expected {expected_compressed_sha}, got {actual_compressed_sha}"
            )

    upload = client.create_multipart_upload(
        Bucket=bucket,
        Key=key,
        ContentType="application/octet-stream",
        Metadata={"source-compressed-sha256": expected_compressed_sha},
    )
    upload_id = upload["UploadId"]
    parts = []
    raw_hash = hashlib.sha256()
    raw_size = 0
    pending = bytearray()
    decompressor = lzma.LZMADecompressor(format=lzma.FORMAT_XZ)

    def send_part(payload):
        number = len(parts) + 1
        response = client.upload_part(
            Bucket=bucket,
            Key=key,
            UploadId=upload_id,
            PartNumber=number,
            Body=payload,
        )
        parts.append({"ETag": response["ETag"], "PartNumber": number})
        print(f"uploaded raw part {number} ({raw_size // (1024 * 1024)} MiB streamed)", flush=True)

    try:
        if source_file:
            with lzma.open(source_file, "rb") as raw:
                while output := raw.read(PART_SIZE):
                    raw_hash.update(output)
                    raw_size += len(output)
                    send_part(output)
        else:
            request = urllib.request.Request(source, headers={"User-Agent": "ai-native-paas-image-import/1.0"})
            with urllib.request.urlopen(request, timeout=180, context=SSL_CONTEXT) as response:
                while chunk := response.read(READ_SIZE):
                    compressed_hash.update(chunk)
                    output = decompressor.decompress(chunk)
                    if output:
                        raw_hash.update(output)
                        raw_size += len(output)
                        pending.extend(output)
                        while len(pending) >= PART_SIZE:
                            send_part(bytes(pending[:PART_SIZE]))
                            del pending[:PART_SIZE]

            actual_compressed_sha = compressed_hash.hexdigest()
            if actual_compressed_sha != expected_compressed_sha:
                raise RuntimeError(
                    f"compressed sha256 mismatch: expected {expected_compressed_sha}, got {actual_compressed_sha}"
                )
            if not decompressor.eof:
                raise RuntimeError("compressed source ended before the xz stream reached EOF")
            if pending:
                send_part(bytes(pending))
        if not parts:
            raise RuntimeError("decompressed image is empty")
        client.complete_multipart_upload(
            Bucket=bucket,
            Key=key,
            UploadId=upload_id,
            MultipartUpload={"Parts": parts},
        )
    except BaseException:
        client.abort_multipart_upload(Bucket=bucket, Key=key, UploadId=upload_id)
        raise

    manifest = {
        "source": source,
        "compressed_sha256": compressed_hash.hexdigest(),
        "raw_sha256": raw_hash.hexdigest(),
        "raw_size": raw_size,
    }
    client.put_object(
        Bucket=bucket,
        Key=key + ".json",
        Body=(json.dumps(manifest, sort_keys=True) + "\n").encode(),
        ContentType="application/json",
    )
    print(f"staged raw image: {raw_size // (1024 * 1024)} MiB, sha256:{raw_hash.hexdigest()}", flush=True)
    return manifest


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", required=True)
    parser.add_argument("--source-file", type=Path)
    parser.add_argument("--compressed-sha256", required=True)
    parser.add_argument("--name", required=True)
    parser.add_argument("--key", required=True)
    parser.add_argument("--description", default="Pinned AI-native PaaS custom image")
    parser.add_argument("--location", default="ru-1")
    parser.add_argument("--poll-seconds", type=int, default=10)
    parser.add_argument("--timeout-seconds", type=int, default=3600)
    parser.add_argument("--keep-staged-object", action="store_true")
    parser.add_argument("--reuse-staged-object", action="store_true")
    args = parser.parse_args()

    token = os.environ.get("TWC_TOKEN", "")
    if not token:
        raise SystemExit("TWC_TOKEN is required")
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
    if args.reuse_staged_object:
        manifest = json.loads(
            client.get_object(Bucket=bucket, Key=args.key + ".json")["Body"].read()
        )
        size = client.head_object(Bucket=bucket, Key=args.key)["ContentLength"]
        if size != manifest["raw_size"]:
            raise RuntimeError(f"staged object size mismatch: expected {manifest['raw_size']}, got {size}")
        if manifest["compressed_sha256"] != args.compressed_sha256:
            raise RuntimeError("staged object compressed sha256 does not match the requested source")
        print(f"reusing verified staged raw image: {size // (1024 * 1024)} MiB", flush=True)
    else:
        manifest = upload_raw(
            client,
            bucket,
            args.key,
            args.source,
            args.compressed_sha256,
            args.source_file,
        )
    upload_url = client.generate_presigned_url(
        "get_object",
        Params={"Bucket": bucket, "Key": args.key},
        ExpiresIn=min(args.timeout_seconds + 1800, 21600),
    )
    # Timeweb validates an import URL by its final suffix. A standards-compliant
    # URI fragment is not sent to S3, but preserves the required .raw suffix
    # after SigV4 query parameters for that validator.
    upload_url += "#" + urllib.parse.quote(Path(args.key).name)
    created = api_request(
        token,
        "POST",
        "/images",
        {
            "name": args.name,
            "description": args.description,
            "upload_url": upload_url,
            "location": args.location,
            "os": "other",
        },
    )["image"]
    image_id = created["id"]
    print(f"Timeweb image {image_id} accepted; waiting for import", flush=True)

    deadline = time.monotonic() + args.timeout_seconds
    last = None
    while time.monotonic() < deadline:
        image = api_request(token, "GET", f"/images/{image_id}")["image"]
        state = (image["status"], image["progress"])
        if state != last:
            print(f"Timeweb image {image_id}: status={state[0]} progress={state[1]}%", flush=True)
            last = state
        if image["status"] == "created":
            if not args.keep_staged_object:
                client.delete_objects(
                    Bucket=bucket,
                    Delete={"Objects": [{"Key": args.key}, {"Key": args.key + ".json"}]},
                )
                print("private staging objects removed", flush=True)
            print(json.dumps({"image_id": image_id, **manifest}, sort_keys=True))
            return
        if image["status"] in {"failed", "deleted"}:
            raise RuntimeError(f"Timeweb custom-image import ended with status={image['status']}")
        time.sleep(args.poll_seconds)
    raise TimeoutError(f"Timeweb custom-image import timed out for image {image_id}")


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(f"image import failed: {error}", file=sys.stderr)
        raise
