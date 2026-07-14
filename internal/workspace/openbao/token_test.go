package openbao

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenBaoWorkloadTokenSupportsKubernetesFSGroupWithoutWorldAccess(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "token")
	const value = "workload-token-0123456789"
	if err := os.WriteFile(filename, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0o400, 0o440, 0o600, 0o640} {
		if err := os.Chmod(filename, mode); err != nil {
			t.Fatal(err)
		}
		token, err := readToken(filename)
		if err != nil || string(token) != value {
			t.Fatalf("secure mode %o rejected: token=%q err=%v", mode, token, err)
		}
		clear(token)
	}
	for _, mode := range []os.FileMode{0o700, 0o660, 0o644, 0o604} {
		if err := os.Chmod(filename, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := readToken(filename); err == nil {
			t.Fatalf("unsafe mode %o accepted", mode)
		}
	}
}
