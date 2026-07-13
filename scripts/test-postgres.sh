#!/usr/bin/env bash
set -Eeuo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

: "${TEST_POSTGRES_DSN:?Set TEST_POSTGRES_DSN, for example: host=127.0.0.1 port=5432 user=postgres password=postgres dbname=kernel_test sslmode=disable}"

CGO_ENABLED=1 go test -race -count=1 -tags=postgres_integration ./test/integration -v
