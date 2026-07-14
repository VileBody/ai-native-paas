package main

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceManager_CredentialFilesSupportKubernetesFSGroupWithoutWorldAccess(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "credential")
	value := []byte("0123456789abcdef0123456789abcdef")
	if err := os.WriteFile(filename, value, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0o400, 0o440, 0o600, 0o640} {
		if err := os.Chmod(filename, mode); err != nil {
			t.Fatal(err)
		}
		loaded, err := readSecureFile(filename, 1024)
		if err != nil || string(loaded) != string(value) {
			t.Fatalf("secure mode %o rejected: value=%q err=%v", mode, loaded, err)
		}
		loaded, err = readExactSecureFile(filename, int64(len(value)))
		if err != nil || string(loaded) != string(value) {
			t.Fatalf("secure exact mode %o rejected: value=%q err=%v", mode, loaded, err)
		}
	}
	for _, mode := range []os.FileMode{0o700, 0o660, 0o644, 0o604} {
		if err := os.Chmod(filename, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := readSecureFile(filename, 1024); err == nil {
			t.Fatalf("unsafe credential mode %o accepted", mode)
		}
	}
}

func TestWorkspaceManager_OpenBaoClientTrustsOnlyConfiguredCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	directory := t.TempDir()
	caFile := filepath.Join(directory, "openbao-ca.crt")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caFile, caPEM, 0o444); err != nil {
		t.Fatal(err)
	}
	client, err := openBaoHTTPClient(caFile)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if err := os.Chmod(caFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caFile, []byte("not a certificate"), 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := openBaoHTTPClient(caFile); err == nil {
		t.Fatal("invalid OpenBao CA accepted")
	}
}
