// Package logstore persists redacted workspace output as client-side encrypted,
// immutable S3 chunks. Plaintext never reaches the object store.
package logstore

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/keir-research/ai-native-paas/internal/workspace"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

type Config struct {
	Endpoint      string
	Region        string
	Bucket        string
	Prefix        string
	AccessKey     string
	SecretKey     string
	EncryptionKey []byte
}

type S3 struct {
	client *minio.Client
	bucket string
	prefix string
	key    [32]byte
}

func NewS3(config Config) (*S3, error) {
	endpoint, err := url.Parse(strings.TrimSpace(config.Endpoint))
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "" && endpoint.Path != "/" || endpoint.Scheme != "https" {
		return nil, errors.New("workspace log S3 endpoint is invalid")
	}
	if strings.TrimSpace(config.Bucket) == "" || strings.TrimSpace(config.AccessKey) == "" || strings.TrimSpace(config.SecretKey) == "" || len(config.EncryptionKey) != 32 {
		return nil, errors.New("workspace log S3 configuration is incomplete")
	}
	client, err := minio.New(endpoint.Host, &minio.Options{
		Creds: credentials.NewStaticV4(config.AccessKey, config.SecretKey, ""), Secure: true,
		Region: config.Region, BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, errors.New("initialize workspace log S3 client")
	}
	store := &S3{client: client, bucket: config.Bucket, prefix: strings.Trim(config.Prefix, "/")}
	copy(store.key[:], config.EncryptionKey)
	return store, nil
}

func (s *S3) PutChunk(ctx context.Context, scope workspace.CommandOutputScope, chunk workspacev1.AgentOutputChunk) error {
	if s == nil || s.client == nil || chunk.Validate() != nil || !validScope(scope) {
		return errors.New("workspace output chunk is invalid")
	}
	objectKey := s.objectKey(scope, chunk)
	metadata := chunkMetadata(chunk)
	if stat, err := s.client.StatObject(ctx, s.bucket, objectKey, minio.StatObjectOptions{}); err == nil {
		if metadataMatches(stat.UserMetadata, metadata) {
			return nil
		}
		return errors.New("workspace output chunk identity changed")
	} else if !isMissing(err) {
		return errors.New("inspect workspace output chunk")
	}
	sealed, err := sealChunk(s.key[:], chunkAAD(scope, chunk), chunk.Data)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, s.bucket, objectKey, bytes.NewReader(sealed), int64(len(sealed)), minio.PutObjectOptions{
		ContentType: "application/octet-stream", UserMetadata: metadata,
	})
	for index := range sealed {
		sealed[index] = 0
	}
	if err != nil {
		return errors.New("persist encrypted workspace output chunk")
	}
	return nil
}

func (s *S3) objectKey(scope workspace.CommandOutputScope, chunk workspacev1.AgentOutputChunk) string {
	scopeHash := sha256.Sum256([]byte(strings.Join([]string{scope.TenantID, scope.ProjectID, scope.WorkspaceID}, "\x00")))
	commandHash := sha256.Sum256([]byte(scope.CommandID))
	return path.Join(s.prefix, hex.EncodeToString(scopeHash[:]), hex.EncodeToString(commandHash[:]), strings.ToLower(string(chunk.Stream)), fmt.Sprintf("%012d.enc", chunk.Sequence))
}

func chunkMetadata(chunk workspacev1.AgentOutputChunk) map[string]string {
	return map[string]string{
		"chunk-sha256": chunk.ChunkSHA256, "final": strconv.FormatBool(chunk.Final),
		"truncated": strconv.FormatBool(chunk.Truncated), "total-sha256": chunk.TotalSHA256,
	}
}

func chunkAAD(scope workspace.CommandOutputScope, chunk workspacev1.AgentOutputChunk) []byte {
	return []byte(strings.Join([]string{
		"workspace-output-v1", scope.TenantID, scope.ProjectID, scope.WorkspaceID, scope.TaskID, scope.CommandID,
		scope.AgentSessionID, scope.VMID, string(chunk.Stream), strconv.FormatInt(chunk.Sequence, 10), chunk.ChunkSHA256,
		strconv.FormatBool(chunk.Final), strconv.FormatBool(chunk.Truncated), chunk.TotalSHA256,
	}, "\x00"))
}

func sealChunk(key, aad, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("initialize workspace output encryption")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("initialize workspace output encryption")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(aad)
	nonce := mac.Sum(nil)[:aead.NonceSize()]
	result := make([]byte, 1+aead.NonceSize(), 1+aead.NonceSize()+len(plaintext)+aead.Overhead())
	result[0] = 1
	copy(result[1:], nonce)
	return aead.Seal(result, nonce, plaintext, aad), nil
}

func validScope(scope workspace.CommandOutputScope) bool {
	for _, value := range []string{scope.TenantID, scope.ProjectID, scope.WorkspaceID, scope.TaskID, scope.CommandID, scope.AgentSessionID, scope.VMID} {
		if value == "" || len(value) > 128 || strings.ContainsAny(value, "/\\\x00") {
			return false
		}
	}
	return true
}

func metadataMatches(stored, wanted map[string]string) bool {
	for name, value := range wanted {
		matched := false
		for storedName, storedValue := range stored {
			if strings.EqualFold(storedName, name) && storedValue == value {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func isMissing(err error) bool {
	response := minio.ToErrorResponse(err)
	return response.StatusCode == 404 || response.Code == "NoSuchKey" || response.Code == "NoSuchObject"
}

var _ workspace.CommandOutputStore = (*S3)(nil)
