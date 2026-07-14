package logstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/workspace"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

func TestWorkspaceLogs_EncryptionIsDeterministicForIdempotentRetryAndHidesPlaintext(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	plaintext := []byte("redacted-output-sentinel")
	digest := sha256.Sum256(plaintext)
	chunk := workspacev1.AgentOutputChunk{
		CommandID: "command-1", Stream: workspacev1.AgentOutputStdout, Sequence: 0, Data: plaintext,
		ChunkSHA256: "sha256:" + hex.EncodeToString(digest[:]), Final: true, TotalSHA256: "sha256:" + hex.EncodeToString(digest[:]),
	}
	scope := workspace.CommandOutputScope{TenantID: "tenant-1", ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", CommandID: "command-1", AgentSessionID: "session-1", VMID: "vm-1"}
	first, err := sealChunk(key, chunkAAD(scope, chunk), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	second, err := sealChunk(key, chunkAAD(scope, chunk), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || bytes.Contains(first, plaintext) {
		t.Fatalf("idempotent ciphertext is unsafe: first=%x second=%x", first, second)
	}
	changed := chunk
	changed.Sequence = 1
	third, _ := sealChunk(key, chunkAAD(scope, changed), plaintext)
	if bytes.Equal(first, third) {
		t.Fatal("different chunk identity reused ciphertext nonce")
	}
}

func TestWorkspaceLogs_RequireHTTPSAndExactEncryptionKey(t *testing.T) {
	if _, err := NewS3(Config{Endpoint: "http://s3.example.com", Bucket: "logs", AccessKey: "access", SecretKey: "secret", EncryptionKey: make([]byte, 32)}); err == nil {
		t.Fatal("plaintext S3 endpoint accepted")
	}
	if _, err := NewS3(Config{Endpoint: "https://s3.example.com", Bucket: "logs", AccessKey: "access", SecretKey: "secret", EncryptionKey: make([]byte, 31)}); err == nil {
		t.Fatal("invalid encryption key accepted")
	}
}
