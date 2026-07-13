#!/usr/bin/env python3
"""Generate the executable evidence map for the agentic DevOps pivot."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from collections import Counter
from pathlib import Path


TEST_RE = re.compile(r"^func\s+((?:Test|Fuzz)[A-Za-z0-9_]+)\s*\(", re.MULTILINE)
TARGETS = {
    "K": "test/pivot/kernel_v2_test.go",
    "S": "test/pivot/source_v2_test.go",
    "B": "test/pivot/build_v2_test.go",
    "R": "test/pivot/runtime_gitops_test.go",
    "A5": "test/pivot/attachments_v2_test.go",
    "C": "test/pivot/commerce_v2_test.go",
    "G": "test/pivot/agent_workspace_test.go",
    "E2E": "test/system/agentic_devops_e2e_test.go",
}
LIVE_MARKERS = (
    "chaos",
    "provider",
    "real ",
    "system",
    "kubernetes",
    "openbao",
    "gitlab",
    "harbor",
    "argo",
    "network",
    "dns",
    "gateway",
)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo-root", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--catalog", type=Path)
    parser.add_argument("--json-output", type=Path)
    parser.add_argument("--markdown-output", type=Path)
    parser.add_argument("--overrides", type=Path)
    parser.add_argument("--check", action="store_true")
    return parser.parse_args()


def defaults(args: argparse.Namespace) -> None:
    root = args.repo_root.resolve()
    args.repo_root = root
    args.catalog = (args.catalog or root / "docs/pivot/tdd-catalog.json").resolve()
    args.json_output = (args.json_output or root / "verification/tdd-matrix.json").resolve()
    args.markdown_output = (
        args.markdown_output or root / "verification/TDD_MATRIX.md"
    ).resolve()
    args.overrides = (args.overrides or root / "docs/pivot/tdd-overrides.json").resolve()


def load_json(path: Path) -> object:
    return json.loads(path.read_text(encoding="utf-8"))


def discover_tests(root: Path) -> dict[str, list[str]]:
    tests: dict[str, list[str]] = {}
    ignored = {"vendor", ".git", ".terraform"}
    for path in sorted(root.rglob("*_test.go")):
        if ignored.intersection(path.parts):
            continue
        relative = path.relative_to(root).as_posix()
        for name in TEST_RE.findall(path.read_text(encoding="utf-8")):
            tests.setdefault(name, []).append(relative)
    return tests


def family(test_id: str) -> str:
    if test_id.startswith("E2E-"):
        return "E2E"
    if test_id.startswith("A5."):
        return "A5"
    return test_id[:1]


def is_live_only(test: dict[str, object]) -> bool:
    if family(str(test["id"])) == "E2E":
        return True
    haystack = " ".join(
        str(test.get(key, "")) for key in ("level", "given", "when", "then")
    ).lower()
    return any(marker in haystack for marker in LIVE_MARKERS)


def load_overrides(path: Path) -> dict[str, dict[str, str]]:
    if not path.exists():
        return {}
    raw = load_json(path)
    if not isinstance(raw, dict):
        raise ValueError("TDD overrides must be an object keyed by requirement ID")
    return {str(key): dict(value) for key, value in raw.items()}


def build_matrix(
    catalog_path: Path,
    tests: dict[str, list[str]],
    overrides: dict[str, dict[str, str]],
) -> dict[str, object]:
    raw = load_json(catalog_path)
    if not isinstance(raw, dict) or not isinstance(raw.get("tests"), list):
        raise ValueError("pivot catalog must contain a tests array")
    catalog_tests = raw["tests"]
    declared = int(raw.get("test_count", -1))
    if declared != len(catalog_tests):
        raise ValueError(f"catalog count mismatch: declared {declared}, found {len(catalog_tests)}")

    requirements: list[dict[str, object]] = []
    ids: set[str] = set()
    for item in catalog_tests:
        if not isinstance(item, dict):
            raise ValueError("catalog test entry must be an object")
        requirement_id = str(item["id"])
        name = str(item["name"])
        if requirement_id in ids:
            raise ValueError(f"duplicate pivot requirement {requirement_id}")
        ids.add(requirement_id)

        status = "REUSED" if name in tests else ("LIVE_ONLY" if is_live_only(item) else "NEW")
        test_name = name
        test_file = tests[name][0] if name in tests else TARGETS[family(requirement_id)]

        override = overrides.get(requirement_id, {})
        status = override.get("status", status)
        test_name = override.get("test_name", test_name)
        discovered_file = tests[test_name][0] if test_name in tests else test_file
        test_file = override.get("test_file", discovered_file)
        if status not in {"REUSED", "NEW", "LIVE_ONLY"}:
            raise ValueError(f"invalid status {status!r} for {requirement_id}")
        if status == "REUSED" and test_name not in tests:
            raise ValueError(f"REUSED requirement {requirement_id} references missing {test_name}")

        requirements.append(
            {
                "id": requirement_id,
                "name": name,
                "family": family(requirement_id),
                "level": str(item.get("level", "")),
                "status": status,
                "test_name": test_name,
                "test_file": test_file,
                "test_ref": f"{test_file}::{test_name}",
            }
        )

    unknown_overrides = sorted(set(overrides) - ids)
    if unknown_overrides:
        raise ValueError(f"overrides reference unknown IDs: {', '.join(unknown_overrides)}")

    statuses = Counter(str(item["status"]) for item in requirements)
    families = Counter(str(item["family"]) for item in requirements)
    digest = hashlib.sha256(catalog_path.read_bytes()).hexdigest()
    return {
        "schema_version": 1,
        "source": "docs/pivot/tdd-catalog.json",
        "source_sha256": digest,
        "pivot_requirement_count": len(requirements),
        "discovered_go_test_count": sum(len(paths) for paths in tests.values()),
        "discovered_unique_test_name_count": len(tests),
        "summary": {
            "statuses": dict(sorted(statuses.items())),
            "families": dict(sorted(families.items())),
            "unmapped": 0,
        },
        "requirements": requirements,
    }


def render_markdown(matrix: dict[str, object]) -> str:
    summary = matrix["summary"]
    requirements = matrix["requirements"]
    assert isinstance(summary, dict) and isinstance(requirements, list)
    statuses = summary["statuses"]
    assert isinstance(statuses, dict)
    lines = [
        "# Agentic DevOps pivot — TDD evidence matrix",
        "",
        "Generated from `docs/pivot/tdd-catalog.json`. Do not edit by hand.",
        "",
        f"- Pivot requirements: **{matrix['pivot_requirement_count']}**",
        f"- Discovered Go tests/fuzz targets: **{matrix['discovered_go_test_count']}**",
        f"- Reused now: **{statuses.get('REUSED', 0)}**",
        f"- New local/contract tests required: **{statuses.get('NEW', 0)}**",
        f"- Live/provider/system tests required: **{statuses.get('LIVE_ONLY', 0)}**",
        "- Unmapped: **0**",
        "",
        "`NEW` and `LIVE_ONLY` are explicit implementation work, not passing evidence.",
        "A release gate may become green only after every referenced test exists and passes.",
        "",
        "| ID | Status | Level | Executable evidence target |",
        "|---|---|---|---|",
    ]
    for item in requirements:
        assert isinstance(item, dict)
        level = str(item["level"]).replace("|", "\\|")
        ref = f"`{item['test_file']}::{item['test_name']}`"
        lines.append(f"| `{item['id']}` | {item['status']} | {level} | {ref} |")
    lines.append("")
    return "\n".join(lines)


def canonical_json(matrix: dict[str, object]) -> str:
    return json.dumps(matrix, ensure_ascii=False, indent=2, sort_keys=True) + "\n"


def check_file(path: Path, expected: str) -> bool:
    if not path.exists():
        print(f"missing generated file: {path}", file=sys.stderr)
        return False
    actual = path.read_text(encoding="utf-8")
    if actual != expected:
        print(f"generated file is stale: {path}", file=sys.stderr)
        return False
    return True


def main() -> int:
    args = parse_args()
    defaults(args)
    tests = discover_tests(args.repo_root)
    matrix = build_matrix(args.catalog, tests, load_overrides(args.overrides))
    json_output = canonical_json(matrix)
    markdown_output = render_markdown(matrix)

    if args.check:
        ok = check_file(args.json_output, json_output)
        ok = check_file(args.markdown_output, markdown_output) and ok
        if ok:
            print(
                f"pivot TDD matrix is current: {matrix['pivot_requirement_count']} requirements, "
                f"{matrix['discovered_go_test_count']} Go tests"
            )
        return 0 if ok else 1

    args.json_output.parent.mkdir(parents=True, exist_ok=True)
    args.markdown_output.parent.mkdir(parents=True, exist_ok=True)
    args.json_output.write_text(json_output, encoding="utf-8")
    args.markdown_output.write_text(markdown_output, encoding="utf-8")
    print(
        f"wrote {matrix['pivot_requirement_count']} requirements to "
        f"{args.json_output.relative_to(args.repo_root)}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
