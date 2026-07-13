#!/usr/bin/env python3
from __future__ import annotations

import hashlib
import json
import os
import shutil
import sys
import zipfile
from datetime import datetime, timezone
from pathlib import Path

root = Path(__file__).resolve().parents[1]
out_root = root.parent
verification = root / ".verification" / "iteration-2"
exit_file = out_root / "iter2-final-verification.exit"
log_file = out_root / "iter2-final-verification.log"

try:
    exit_code = int(exit_file.read_text(encoding="utf-8").strip())
except Exception:
    exit_code = 999

status = "PASS" if exit_code == 0 else "FAIL"

steps: list[tuple[str, str, str]] = []
status_tsv = verification / "status.tsv"
if status_tsv.exists():
    for raw in status_tsv.read_text(encoding="utf-8", errors="replace").splitlines():
        if not raw.strip():
            continue
        cols = raw.split("\t")
        name = cols[0].strip()
        result = cols[1].strip() if len(cols) > 1 else "UNKNOWN"
        detail = "\t".join(cols[2:]).strip() if len(cols) > 2 else ""
        steps.append((name, result, detail))

failed = [s for s in steps if s[1].upper() not in {"PASS", "OK", "SUCCESS", "0"}]

status_doc = root / "docs" / "iteration-2" / "FINAL_STATUS.md"
status_doc.parent.mkdir(parents=True, exist_ok=True)
lines = [
    "# Iteration 2 — final machine verdict",
    "",
    f"**Verdict: `{status}`**",
    "",
    f"Verification exit code: `{exit_code}`",
    "",
    f"Generated: `{datetime.now(timezone.utc).isoformat()}`",
    "",
]
if steps:
    lines.extend(["## Gate steps", ""])
    for name, result, detail in steps:
        suffix = f" — {detail}" if detail else ""
        lines.append(f"- `{name}`: **{result}**{suffix}")
    lines.append("")
if failed:
    lines.extend(["## Failed or incomplete steps", ""])
    for name, result, detail in failed:
        suffix = f": {detail}" if detail else ""
        lines.append(f"- `{name}` ({result}){suffix}")
    lines.append("")
else:
    lines.extend([
        "No failed gate step was recorded.",
        "",
    ])
lines.extend([
    "The raw verification log is packaged as `docs/iteration-2/VERIFICATION.log`.",
    "",
])
status_doc.write_text("\n".join(lines), encoding="utf-8")

# Preserve the exact final verification log inside the deliverable.
if log_file.exists():
    shutil.copy2(log_file, root / "docs" / "iteration-2" / "VERIFICATION.log")

# Write a conservative TODO that distinguishes missing proof from later-scope work.
todo = root / "docs" / "iteration-2" / "TODO.md"
existing = todo.read_text(encoding="utf-8", errors="replace") if todo.exists() else ""
marker = "<!-- FINALIZER-TODO-V1 -->"
base = existing.split(marker)[0].rstrip()
extra = [
    "",
    marker,
    "",
    "# Explicit remaining TODO",
    "",
]
if status != "PASS":
    extra.extend([
        "## Verification failures to repair in Iteration 2",
        "",
        "The final gate did not pass. Treat every item below as unfinished Iteration 2 work, not as debt to be hidden in a later iteration:",
        "",
    ])
    if failed:
        for name, result, detail in failed:
            suffix = f" — {detail}" if detail else ""
            extra.append(f"- [ ] Repair `{name}` (`{result}`){suffix}")
    else:
        extra.append("- [ ] Inspect `VERIFICATION.log`; the verification process exited non-zero before recording structured step status.")
    extra.append("")

extra.extend([
    "## Not proven against external production services",
    "",
    "- [ ] Run the GitLab adapter contract suite against the exact target GitLab Self-Managed version, not only the deterministic REST stub.",
    "- [ ] Confirm and freeze the exact webhook signature headers/canonicalization supported by that GitLab deployment; keep legacy `X-Gitlab-Token` opt-in only.",
    "- [ ] Run PostgreSQL tests against the exact production PostgreSQL major version and connection-pool/driver configuration used by the control plane.",
    "- [ ] Exercise GitLab rename, transfer, archived project, protected branch, rate-limit, 429, 5xx, and timeout behavior against a live instance.",
    "- [ ] Exercise Git LFS and explicitly supported submodule policies against a live GitLab instance.",
    "",
    "## Security and abuse hardening before public exposure",
    "",
    "- [ ] Run workspace workers in a dedicated sandbox with filesystem, inode, PID, CPU, memory, wall-clock, and egress limits.",
    "- [ ] Add repository/LFS size quotas and maximum changed-file/byte limits.",
    "- [ ] Add secret scanning and malware policy for agent-authored commits.",
    "- [ ] Store webhook keys and ephemeral Git credentials through the platform secrets broker; test rotation and revocation under failure.",
    "- [ ] Add API rate limiting and tenant-scoped abuse controls.",
    "",
    "## Resilience and scale tests",
    "",
    "- [ ] Soak-test concurrent provisioning, webhook storms, reconciliation, and workspace cleanup at expected production cardinality.",
    "- [ ] Inject GitLab latency, connection resets, ambiguous timeouts, database failover, deadlocks, serialization failures, and worker termination.",
    "- [ ] Benchmark indexes and reconciliation claim queries on production-scale tables.",
    "- [ ] Prove backup/restore of the `source` schema and replay of outbox/inbox state.",
    "",
    "## Deliberately deferred to later domains",
    "",
    "These are not Iteration 2 defects:",
    "",
    "- source-to-image build execution and artifact signing — Iteration 3;",
    "- release/deployment/Argo/PaaSApp lifecycle — Iteration 4;",
    "- runtime/build secrets, managed services and domains — Iteration 5;",
    "- quota/rating/billing — Iteration 6;",
    "- MCP scopes, approvals and autonomous action budgets — Iteration 7.",
    "",
])
todo.write_text(base + "\n" + "\n".join(extra), encoding="utf-8")

# Remove generated executable from deliverable; source and logs remain.
for candidate in [verification / "source-api", root / "source-api"]:
    try:
        if candidate.is_file():
            candidate.unlink()
    except OSError:
        pass

# Generate checksums for all relevant files, excluding transient VCS/cache artifacts.
def include(path: Path) -> bool:
    rel = path.relative_to(root)
    parts = set(rel.parts)
    if ".git" in parts or ".cache" in parts:
        return False
    if rel.as_posix() in {"MANIFEST.sha256"}:
        return False
    if path.is_symlink():
        return False
    return path.is_file()

files = sorted([p for p in root.rglob("*") if include(p)], key=lambda p: p.relative_to(root).as_posix())
manifest_lines = []
for p in files:
    h = hashlib.sha256()
    with p.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    manifest_lines.append(f"{h.hexdigest()}  {p.relative_to(root).as_posix()}")
(root / "MANIFEST.sha256").write_text("\n".join(manifest_lines) + "\n", encoding="utf-8")

metadata = {
    "iteration": 2,
    "domain": "Source Control",
    "verification_status": status,
    "verification_exit_code": exit_code,
    "generated_at": datetime.now(timezone.utc).isoformat(),
    "structured_steps": [
        {"name": n, "status": s, "detail": d} for n, s, d in steps
    ],
}
(root / "ITERATION_2_RESULT.json").write_text(json.dumps(metadata, indent=2) + "\n", encoding="utf-8")

archive = out_root / "ai-native-paas-iteration-2.zip"
if archive.exists():
    archive.unlink()
with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as zf:
    for p in sorted(root.rglob("*"), key=lambda p: p.relative_to(root).as_posix()):
        if not p.is_file() or p.is_symlink():
            continue
        rel = p.relative_to(root)
        if ".git" in rel.parts or ".cache" in rel.parts:
            continue
        zf.write(p, Path(root.name) / rel)

h = hashlib.sha256()
with archive.open("rb") as f:
    for chunk in iter(lambda: f.read(1024 * 1024), b""):
        h.update(chunk)
(out_root / "ai-native-paas-iteration-2.zip.sha256").write_text(
    f"{h.hexdigest()}  {archive.name}\n", encoding="utf-8"
)

# Stable marker files make the verdict machine-readable without parsing prose.
for name in ["ITERATION_2_PASS", "ITERATION_2_FAIL"]:
    p = out_root / name
    if p.exists():
        p.unlink()
(out_root / f"ITERATION_2_{status}").write_text(status + "\n", encoding="utf-8")

print(json.dumps(metadata, indent=2))
print(str(archive))
