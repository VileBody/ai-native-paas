"""Load Timeweb S3 credentials from operator-managed files only.

The Timeweb main S3 user is account-wide.  It must never become a convenient
runtime credential simply because a Terraform S3 bucket resource exposes it as
an attribute.  These helpers deliberately have no fallback to OpenTofu output
or environment-variable secret values.
"""

from __future__ import annotations

import os
from pathlib import Path
import stat


def read_secret_file(environment_name: str) -> str:
    """Return a single S3 credential from a private regular file.

    The file path, not the credential value, is supplied through the
    environment.  Reject group/world-readable files and line-oriented values
    so a shell trace or accidental multiline paste cannot silently widen the
    secret's exposure.
    """

    value = os.environ.get(environment_name, "")
    if not value:
        raise SystemExit(f"{environment_name} must name a private credential file")
    path = Path(value).expanduser()
    try:
        metadata = path.lstat()
    except FileNotFoundError as error:
        raise SystemExit(f"{environment_name} file does not exist") from error
    if not stat.S_ISREG(metadata.st_mode) or path.is_symlink():
        raise SystemExit(f"{environment_name} must be a regular file")
    if metadata.st_mode & (stat.S_IRWXG | stat.S_IRWXO):
        raise SystemExit(f"{environment_name} file must not be group/world accessible")
    secret = path.read_text(encoding="utf-8").strip()
    if not secret or any(character.isspace() for character in secret):
        raise SystemExit(f"{environment_name} must contain one non-whitespace credential")
    return secret


def read_s3_pair(access_key_file_env: str, secret_key_file_env: str) -> tuple[str, str]:
    """Load a Timeweb S3 access/secret pair without printing either value."""

    return read_secret_file(access_key_file_env), read_secret_file(secret_key_file_env)
