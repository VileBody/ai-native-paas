#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
config="${GITLAB_LIVE_CONFIG:-${repo_root}/.state-backend/gitlab-beta.env}"

if [[ ! -f "${config}" || -L "${config}" ]]; then
  printf 'GitLab live config must be a regular file: %s\n' "${config}" >&2
  exit 2
fi
if [[ "$(stat -f '%Lp' "${config}" 2>/dev/null || stat -c '%a' "${config}")" != "600" ]]; then
  printf 'GitLab live config must have mode 600\n' >&2
  exit 2
fi

set -a
# shellcheck disable=SC1090
source "${config}"
set +a
: "${GITLAB_ADMIN_TOKEN:?GITLAB_ADMIN_TOKEN is required}"
: "${GITLAB_NAMESPACE_ID:?GITLAB_NAMESPACE_ID is required}"

export GITLAB_LIVE=1
cd "${repo_root}"
go test -count=1 -run '^TestGitLabReal_ProvisionCommitObserveArchive$' ./internal/source/gitlab
printf 'GitLab private repository lifecycle live gate: PASS\n'
