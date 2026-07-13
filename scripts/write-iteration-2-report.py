#!/usr/bin/env python3
from pathlib import Path
import json, re, sys, datetime
root=Path(sys.argv[1]); out=Path(sys.argv[2]); failed=int(sys.argv[3])
rows=[]
for line in (out/'status.tsv').read_text().splitlines():
    parts=line.split('\t');
    if len(parts)>=3: rows.append(parts[:3])
passes=sum(1 for r in rows if r[1]=='PASS'); failures=sum(1 for r in rows if r[1].startswith('FAIL')); skips=sum(1 for r in rows if r[1].startswith('SKIP'))
tests=passed=skipped=0; packages=set()
json_path=out/'test-default.json'
if json_path.exists():
    for line in json_path.read_text(errors='replace').splitlines():
        try: e=json.loads(line)
        except Exception: continue
        if e.get('Test') and e.get('Action') in ('pass','fail','skip'):
            # Count top-level tests/fuzz seeds only, not slash subtests.
            if '/' not in e['Test']:
                tests+=1; passed+=e['Action']=='pass'; skipped+=e['Action']=='skip'
        if e.get('Package') and e.get('Action')=='pass': packages.add(e['Package'])
coverage='unavailable'
cov=out/'coverage.txt'
if cov.exists():
    m=re.search(r'total:\s+\(statements\)\s+([0-9.]+%)',cov.read_text(errors='replace'))
    if m: coverage=m.group(1)
status='GREEN' if failed==0 else 'RED'
report=f'''# Iteration 2 — Source Control test report

Generated: {datetime.datetime.now(datetime.timezone.utc).isoformat()}

## Verdict

**{status}**

- verification steps passed: **{passes}**
- failed: **{failures}**
- skipped: **{skips}**
- top-level tests observed in default suite: **{tests}**
- top-level tests passed: **{passed}**
- test packages passed: **{len(packages)}**
- statement coverage: **{coverage}**

## Verification matrix

| Step | Result | Seconds |
|---|---:|---:|
'''
for name,result,seconds in rows: report+=f'| `{name}` | **{result}** | {seconds} |\n'
report+='''
## Tested boundaries

- domain invariants for projects, repositories, branch heads, merge requests and workspaces;
- command idempotency and concurrent duplicate creation;
- GitLab provisioning recovery after a lost response;
- immutable numeric provider identity across renames;
- Standard Webhooks HMAC verification, body integrity and replay window;
- legacy GitLab token only behind explicit compatibility configuration;
- duplicate and out-of-order webhook handling;
- periodic reconciliation of missed source events;
- path traversal, `.git` mutation and symlink escape rejection;
- exact-SHA clone, commit, force-with-lease push and lost push-response recovery on real local Git;
- credential revocation retry without a second push;
- GitLab REST v4 contract through an HTTP stub;
- end-to-end source acceptance across GitLab adapter, local Git workspace, signed webhook and inbox dedupe;
- PostgreSQL schema/migrations, atomic rollback, concurrent dedupe, optimistic locking, append-only audit and immutable provider identity when the live suite is marked PASS;
- public HTTP boundary, tenant derivation from resource paths and raw-byte webhook verification.

Raw logs and machine-readable results are under `.verification/iteration-2/`.
'''
docs=root/'docs'/'iteration-2';docs.mkdir(parents=True,exist_ok=True);(docs/'TEST_REPORT.md').write_text(report)
# Only untested/unfinished work belongs here; failures remain failures in TEST_REPORT rather than being laundered into TODO.
todo=['# Iteration 2 — TODO / verification debt','']
if any(r[0]=='postgres-live' and r[1].startswith('SKIP') for r in rows):
    todo += ['## Unexecuted in this environment','', '- Run the tagged live PostgreSQL suite with `TEST_POSTGRES_DSN` or `scripts/start-test-postgres.sh`.','']
else:
    todo += ['## Unexecuted in this environment','', '- None of the planned Iteration 2 test layers were skipped.','']
todo += ['## Deliberately deferred beyond Iteration 2','',
'- Production OIDC authentication remains owned by Platform Kernel/deployment wiring; the Source HTTP test harness uses verified principal headers.',
'- GitLab HA, Gitaly object-storage backups and restore drills are infrastructure/operations work, not Source Control domain behavior.',
'- Kernel-level rate limiting and global abuse controls are deferred to public-edge hardening.',
'- The standard-library workspace guard performs repeated `Lstat` checks; Linux `openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS)` hardening is recommended before hostile public workloads.',
'- Real GitLab version-matrix testing should be added in CI against the exact self-managed version selected for production.',
'- Long-running soak and fault-injection tests for GitLab/network outages remain operational hardening.',
'']
(docs/'TODO.md').write_text('\n'.join(todo))
