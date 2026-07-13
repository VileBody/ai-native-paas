#!/usr/bin/env bash
set -Eeuo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

make fmt-check
make vet
make test
make race
make integration-compile
make fuzz FUZZTIME="${FUZZTIME:-5s}"
make coverage-html
make build

echo "Iteration 1 verification completed successfully."
