#!/usr/bin/env python3
"""Verify exact Iteration 4 TDD test-name coverage and write an optional matrix."""
from __future__ import annotations

import argparse
import json
import re
from pathlib import Path

TEST_RE = re.compile(r"\bTest[A-Za-z0-9_]+")
FUNC_RE = re.compile(r"(?m)^func\s+(Test[A-Za-z0-9_]+)\s*\(")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", default=".")
    parser.add_argument("--matrix")
    parser.add_argument("--json")
    args = parser.parse_args()

    root = Path(args.root).resolve()
    spec_path = root / "docs/tdd/04-runtime-delivery.md"
    required: list[str] = []
    for name in TEST_RE.findall(spec_path.read_text(encoding="utf-8")):
        if name not in required:
            required.append(name)

    locations: dict[str, list[str]] = {}
    for path in sorted(root.rglob("*_test.go")):
        relative = path.relative_to(root).as_posix()
        for name in FUNC_RE.findall(path.read_text(encoding="utf-8", errors="ignore")):
            locations.setdefault(name, []).append(relative)

    missing = [name for name in required if name not in locations]
    rows = [(name, locations.get(name, [])) for name in required]
    payload = {
        "iteration": 4,
        "required": len(required),
        "present": len(required) - len(missing),
        "missing": missing,
        "tests": [{"name": name, "locations": locs} for name, locs in rows],
    }

    if args.matrix:
        target = Path(args.matrix)
        target.parent.mkdir(parents=True, exist_ok=True)
        lines = [
            "# Iteration 4 TDD matrix",
            "",
            f"Required: **{len(required)}**  ",
            f"Present: **{len(required) - len(missing)}**  ",
            f"Missing: **{len(missing)}**",
            "",
            "| Status | Test | Implementation |",
            "|---|---|---|",
        ]
        for name, locs in rows:
            status = "PASS" if locs else "MISSING"
            implementation = "<br>".join(f"`{item}`" for item in locs) if locs else "—"
            lines.append(f"| {status} | `{name}` | {implementation} |")
        target.write_text("\n".join(lines) + "\n", encoding="utf-8")

    if args.json:
        target = Path(args.json)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")

    print(f"TDD names present: {payload['present']}/{payload['required']}")
    if missing:
        print("Missing:")
        for name in missing:
            print(name)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
