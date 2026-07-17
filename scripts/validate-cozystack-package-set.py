#!/usr/bin/env python3
"""Fail closed unless a rendered Cozystack Package set matches its profile."""

import argparse
import json
import pathlib
import re
import sys


def rendered_packages(raw: str) -> set[str]:
    result: set[str] = set()
    lines = raw.splitlines()
    for index, line in enumerate(lines):
        if not re.match(r"^\s*kind:\s*Package\s*$", line):
            continue

        metadata_indent: int | None = None
        for metadata_index in range(index + 1, min(index + 80, len(lines))):
            metadata_line = lines[metadata_index]
            if re.match(r"^\s*kind:\s*\S+\s*$", metadata_line):
                break
            if not re.match(r"^\s*metadata:\s*$", metadata_line):
                continue

            metadata_indent = len(metadata_line) - len(metadata_line.lstrip(" \t"))
            for name_index in range(metadata_index + 1, min(metadata_index + 80, len(lines))):
                name_line = lines[name_index]
                if not name_line.strip():
                    continue
                name_indent = len(name_line) - len(name_line.lstrip(" \t"))
                if name_indent <= metadata_indent and re.match(r"^\s*[A-Za-z][A-Za-z0-9_-]*:", name_line):
                    break
                match = re.match(r"^\s+name:\s*([^\s#]+)", name_line)
                if match:
                    result.add(match.group(1).strip('"\''))
                    break
            break

        if metadata_indent is None:
            raise ValueError("rendered Package has no metadata")
    return result


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--profile", required=True, choices=("smoke", "provider_gate"))
    parser.add_argument("--manifest", required=True, type=pathlib.Path)
    parser.add_argument(
        "--policy",
        type=pathlib.Path,
        default=pathlib.Path(__file__).parents[1]
        / "infra/stacks/cozystack-lab/packages/profile-policy.json",
    )
    args = parser.parse_args()

    policy = json.loads(args.policy.read_text(encoding="utf-8"))
    if policy.get("cozystack_version") != "v1.5.0" or not policy.get("deny_unknown_packages"):
        raise ValueError("package policy must pin v1.5.0 and deny unknown packages")

    expected = set(policy["profiles"][args.profile]["allowed_packages"])
    actual = rendered_packages(args.manifest.read_text(encoding="utf-8"))
    actual -= set(policy.get("root_packages", []))
    forbidden = actual.intersection(policy["always_forbidden"])
    missing = expected - actual
    unknown = actual - expected
    if forbidden or missing or unknown:
        print(
            json.dumps(
                {
                    "profile": args.profile,
                    "forbidden": sorted(forbidden),
                    "missing": sorted(missing),
                    "unknown": sorted(unknown),
                },
                indent=2,
            ),
            file=sys.stderr,
        )
        return 1
    print(json.dumps({"profile": args.profile, "packages": sorted(actual)}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
