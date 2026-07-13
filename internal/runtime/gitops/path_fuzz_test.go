package gitops

import (
	"path/filepath"
	"strings"
	"testing"
)

func FuzzGitOpsPathValidationCannotEscape(f *testing.F) {
	f.Add("cells/cell-a/tenants/tenant-a/apps/app-a/production", "cell-a")
	f.Add("../../etc/passwd", "cell-a")
	f.Add("cells/cell-a/tenants/tenant-a/apps/app-a/../production", "cell-a")
	f.Add("cells/cell-b/tenants/tenant-a/apps/app-a/production", "cell-a")

	f.Fuzz(func(t *testing.T, value, cellID string) {
		if len(value) > 4096 || len(cellID) > 256 {
			t.Skip()
		}
		if err := validateBundlePath(value, cellID); err != nil {
			return
		}
		if filepath.IsAbs(value) || strings.Contains(value, "\\") {
			t.Fatalf("accepted non-portable absolute path %q", value)
		}
		clean := filepath.ToSlash(filepath.Clean(value))
		if clean != value || clean == ".." || strings.HasPrefix(clean, "../") {
			t.Fatalf("accepted escaping or non-canonical path %q -> %q", value, clean)
		}
		parts := strings.Split(value, "/")
		if len(parts) != 7 || parts[0] != "cells" || parts[1] != cellID || parts[2] != "tenants" || parts[4] != "apps" {
			t.Fatalf("accepted path outside platform layout: %q", value)
		}
	})
}
