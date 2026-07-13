#!/usr/bin/env python3
from __future__ import annotations

import argparse
import re
from pathlib import Path

TEST_RE = re.compile(r"\b(Test[A-Za-z0-9_]+)\b")
DECL_RE = re.compile(r"^func\s+(Test[A-Za-z0-9_]+)\s*\(", re.MULTILINE)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", default=".")
    args = parser.parse_args()
    root = Path(args.root).resolve()
    spec = root / "docs/tdd/05-application-attachments.md"
    required = sorted(set(TEST_RE.findall(spec.read_text(encoding="utf-8"))))
    declared: set[str] = set()
    for path in root.rglob("*_test.go"):
        if "/vendor/" in path.as_posix():
            continue
        declared.update(DECL_RE.findall(path.read_text(encoding="utf-8")))
    missing = sorted(set(required) - declared)
    print(f"Iteration 5 TDD parity: {len(required) - len(missing)}/{len(required)}")
    if missing:
        print("Missing:")
        for name in missing:
            print(f"  {name}")
        return 1
    if len(required) != 68:
        print(f"Expected 68 required tests in specification, found {len(required)}")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
