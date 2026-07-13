#!/usr/bin/env python3
from __future__ import annotations

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SPEC = ROOT / "docs/tdd/06-commercial-governance.md"
SOURCE_GLOBS = [
    "internal/commerce/**/*_test.go",
    "test/acceptance/commercial_governance_acceptance_test.go",
    "test/contract/commerce_v1_test.go",
    "test/integration/postgres_commerce_test.go",
]

spec_text = SPEC.read_text(encoding="utf-8")
required = sorted(set(re.findall(r"\b(Test[A-Za-z0-9_]+)\b", spec_text)))
implemented: set[str] = set()
for pattern in SOURCE_GLOBS:
    for path in ROOT.glob(pattern):
        text = path.read_text(encoding="utf-8")
        implemented.update(re.findall(r"^func\s+(Test[A-Za-z0-9_]+)\s*\(", text, flags=re.MULTILINE))
missing = sorted(set(required) - implemented)
result = {
    "required": len(required),
    "present": len(required) - len(missing),
    "missing": missing,
}
print(json.dumps(result, indent=2, sort_keys=True))
if missing:
    sys.exit(1)
