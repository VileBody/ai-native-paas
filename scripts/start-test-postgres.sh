#!/usr/bin/env bash
set -euo pipefail
PORT=${PAAS_TEST_POSTGRES_PORT:-55432}
DATA=${PAAS_TEST_POSTGRES_DATA:-/tmp/paas-iteration6-pgdata}
SOCK=${PAAS_TEST_POSTGRES_SOCKET:-/tmp/paas-iteration6-pgsock}
LOG=${PAAS_TEST_POSTGRES_LOG:-/tmp/paas-iteration6-postgres.log}
DB=${PAAS_TEST_POSTGRES_DB:-postgres}
find_bindir() {
  if [[ -n "${PAAS_TEST_POSTGRES_BINDIR:-}" && -x "${PAAS_TEST_POSTGRES_BINDIR}/initdb" ]]; then
    printf '%s\n' "$PAAS_TEST_POSTGRES_BINDIR"
    return
  fi
  if command -v pg_config >/dev/null 2>&1; then
    local configured
    configured=$(pg_config --bindir 2>/dev/null || true)
    if [[ -n "$configured" && -x "$configured/initdb" ]]; then
      printf '%s\n' "$configured"
      return
    fi
  fi
  find /usr/lib/postgresql /usr/local /opt /mnt/data -mindepth 2 -maxdepth 6 -type f -name initdb -printf '%h\n' 2>/dev/null | sort -V | tail -1
}
BINDIR=$(find_bindir || true)
if [[ -z "$BINDIR" || ! -x "$BINDIR/initdb" ]]; then
  echo "PostgreSQL server binaries not found" >&2
  exit 88
fi
rm -rf "$DATA" "$SOCK"
mkdir -p "$DATA" "$SOCK"
DB_ENV=()
if [[ -n "${PAAS_TEST_POSTGRES_LD_LIBRARY_PATH:-}" ]]; then
  DB_ENV+=("LD_LIBRARY_PATH=${PAAS_TEST_POSTGRES_LD_LIBRARY_PATH}")
fi
run_as_db_user() { env "${DB_ENV[@]}" "$@"; }
if [[ $(id -u) -eq 0 ]]; then
  id postgres >/dev/null 2>&1 || useradd -m postgres
  chown -R postgres:postgres "$DATA" "$SOCK"
  run_as_db_user() { runuser -u postgres -- env "${DB_ENV[@]}" "$@"; }
fi
INITDB_ARGS=("$BINDIR/initdb" -D "$DATA" -A trust -U postgres --no-locale)
if [[ -n "${PAAS_TEST_POSTGRES_SHAREDIR:-}" ]]; then
  INITDB_ARGS+=( -L "$PAAS_TEST_POSTGRES_SHAREDIR" )
fi
run_as_db_user "${INITDB_ARGS[@]}" >/dev/null
run_as_db_user "$BINDIR/pg_ctl" -D "$DATA" -l "$LOG" -o "-p $PORT -k $SOCK -c listen_addresses=127.0.0.1 -c fsync=off -c synchronous_commit=off" start >/dev/null
cleanup() { run_as_db_user "$BINDIR/pg_ctl" -D "$DATA" -m fast stop >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM
ready=0
for _ in $(seq 1 100); do
  if [[ -x "$BINDIR/pg_isready" ]]; then
    env "${DB_ENV[@]}" "$BINDIR/pg_isready" -h 127.0.0.1 -p "$PORT" -U postgres >/dev/null 2>&1 && ready=1 && break
  elif (exec 3<>"/dev/tcp/127.0.0.1/$PORT") >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep .1
done
if [[ $ready -ne 1 ]]; then
  echo "PostgreSQL did not become ready; see $LOG" >&2
  exit 89
fi
if [[ "$DB" != "postgres" ]]; then
  if [[ -x "$BINDIR/createdb" ]]; then
    env "${DB_ENV[@]}" "$BINDIR/createdb" -h 127.0.0.1 -p "$PORT" -U postgres "$DB" 2>/dev/null || true
  else
    echo "createdb is unavailable in $BINDIR; use PAAS_TEST_POSTGRES_DB=postgres" >&2
    exit 90
  fi
fi
export PATH="$BINDIR:$PATH"
if [[ -n "${PAAS_TEST_POSTGRES_LD_LIBRARY_PATH:-}" ]]; then export LD_LIBRARY_PATH="$PAAS_TEST_POSTGRES_LD_LIBRARY_PATH"; fi
export TEST_POSTGRES_DSN="postgresql://postgres@127.0.0.1:${PORT}/${DB}?sslmode=disable"
if [[ $# -eq 0 ]]; then
  echo "$TEST_POSTGRES_DSN"
  while true; do sleep 3600; done
else
  "$@"
fi
