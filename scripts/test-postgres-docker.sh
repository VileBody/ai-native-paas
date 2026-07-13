#!/usr/bin/env bash
set -Eeuo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

port="${PAAS_TEST_POSTGRES_PORT:-55432}"
compose=(docker compose -f compose.test.yaml)

"${compose[@]}" up -d --wait postgres

if [[ "$(uname -s)" == "Darwin" ]] && command -v brew >/dev/null 2>&1; then
  libpq_prefix="$(brew --prefix libpq 2>/dev/null || true)"
  if [[ -n "$libpq_prefix" ]]; then
    export PATH="$libpq_prefix/bin:$PATH"
    export PKG_CONFIG_PATH="$libpq_prefix/lib/pkgconfig${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
  fi
fi

if ! command -v pkg-config >/dev/null 2>&1 || ! pkg-config --exists libpq; then
  echo "libpq development files are required by the PostgreSQL integration test driver" >&2
  echo "macOS: brew install libpq pkg-config" >&2
  echo "Debian/Ubuntu: apt-get install libpq-dev pkg-config" >&2
  exit 2
fi

export TEST_POSTGRES_DSN="${TEST_POSTGRES_DSN:-postgresql://postgres:postgres@127.0.0.1:${port}/paas_test?sslmode=disable}"
make test-postgres
