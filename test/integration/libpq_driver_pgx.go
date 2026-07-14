//go:build postgres_integration && !cgo

package integration_test

import (
	"database/sql"

	"github.com/jackc/pgx/v5/stdlib"
)

// The integration suite historically uses the local driver name
// "kernel_libpq". Register pgx under the same test-only name when CGO/libpq is
// unavailable so contributors and Kubernetes jobs can run the PostgreSQL gates
// without a native toolchain or Docker daemon.
func init() {
	sql.Register("kernel_libpq", stdlib.GetDefaultDriver())
}
