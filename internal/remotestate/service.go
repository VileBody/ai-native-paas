// Package remotestate implements the governed OpenTofu HTTP state backend.
package remotestate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	ErrNotFound          = errors.New("state not found")
	ErrLocked            = errors.New("state is locked")
	ErrStaleLock         = errors.New("stale state lock requires recovery")
	ErrLockMismatch      = errors.New("state lock does not match")
	ErrRecoveryForbidden = errors.New("state lock recovery is forbidden")
)

var namespacePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,126}[a-z0-9])?$`)

type Lock struct {
	ID        string    `json:"ID"`
	Operation string    `json:"Operation,omitempty"`
	Info      string    `json:"Info,omitempty"`
	Who       string    `json:"Who,omitempty"`
	Version   string    `json:"Version,omitempty"`
	Created   time.Time `json:"Created,omitempty"`
	Path      string    `json:"Path,omitempty"`
}

type Blob struct {
	Data      []byte
	VersionID string
	ETag      string
}

type BlobStore interface {
	Get(context.Context, string) (Blob, error)
	Put(context.Context, string, []byte) (Blob, error)
	Delete(context.Context, string) error
}

type Repository interface {
	Acquire(context.Context, string, Lock, string) (*Lock, error)
	Verify(context.Context, string, string) error
	Release(context.Context, string, string) error
	Recover(context.Context, string, string, string, string) error
	RecordState(context.Context, string, Blob, string) error
}

type RecoveryAuthorization struct {
	Allowed bool
	Actor   string
	Reason  string
}

type Service struct {
	blobs      BlobStore
	repository Repository
}

func NewService(blobs BlobStore, repository Repository) (*Service, error) {
	if blobs == nil || repository == nil {
		return nil, errors.New("remote state requires blob store and repository")
	}
	return &Service{blobs: blobs, repository: repository}, nil
}

func ValidateNamespace(namespace string) error {
	if !namespacePattern.MatchString(namespace) || strings.Contains(namespace, "..") {
		return errors.New("invalid state namespace")
	}
	return nil
}

func (s *Service) Get(ctx context.Context, namespace string) (Blob, error) {
	if err := ValidateNamespace(namespace); err != nil {
		return Blob{}, err
	}
	return s.blobs.Get(ctx, namespace)
}

func (s *Service) Put(ctx context.Context, namespace, lockID, actor string, data []byte) (Blob, error) {
	if err := ValidateNamespace(namespace); err != nil {
		return Blob{}, err
	}
	if lockID == "" {
		return Blob{}, ErrLockMismatch
	}
	if err := validateEncryptedState(data); err != nil {
		return Blob{}, err
	}
	if err := s.repository.Verify(ctx, namespace, lockID); err != nil {
		return Blob{}, err
	}
	blob, err := s.blobs.Put(ctx, namespace, data)
	if err != nil {
		return Blob{}, fmt.Errorf("write encrypted state blob: %w", err)
	}
	if err := s.repository.RecordState(ctx, namespace, blob, actor); err != nil {
		return Blob{}, fmt.Errorf("record state metadata: %w", err)
	}
	return blob, nil
}

func (s *Service) Delete(ctx context.Context, namespace, lockID, actor string) error {
	if err := ValidateNamespace(namespace); err != nil {
		return err
	}
	if err := s.repository.Verify(ctx, namespace, lockID); err != nil {
		return err
	}
	if err := s.blobs.Delete(ctx, namespace); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("delete encrypted state blob: %w", err)
	}
	return s.repository.RecordState(ctx, namespace, Blob{}, actor)
}

func (s *Service) Acquire(ctx context.Context, namespace, actor string, lock Lock) (*Lock, error) {
	if err := ValidateNamespace(namespace); err != nil {
		return nil, err
	}
	lock.ID = strings.TrimSpace(lock.ID)
	if lock.ID == "" || len(lock.ID) > 256 {
		return nil, errors.New("lock ID is required")
	}
	lock.Path = namespace
	return s.repository.Acquire(ctx, namespace, lock, actor)
}

func (s *Service) Release(ctx context.Context, namespace, lockID string) error {
	if err := ValidateNamespace(namespace); err != nil {
		return err
	}
	if strings.TrimSpace(lockID) == "" {
		return ErrLockMismatch
	}
	return s.repository.Release(ctx, namespace, lockID)
}

func (s *Service) RecoverStale(ctx context.Context, namespace, lockID string, authorization RecoveryAuthorization) error {
	if err := ValidateNamespace(namespace); err != nil {
		return err
	}
	if !authorization.Allowed {
		return ErrRecoveryForbidden
	}
	lockID = strings.TrimSpace(lockID)
	actor := strings.TrimSpace(authorization.Actor)
	reason := strings.TrimSpace(authorization.Reason)
	if lockID == "" || actor == "" || reason == "" || len(reason) > 2048 {
		return errors.New("stale lock recovery requires lock ID, actor, and bounded audit reason")
	}
	return s.repository.Recover(ctx, namespace, lockID, actor, reason)
}

func validateEncryptedState(data []byte) error {
	if len(data) == 0 || len(data) > 64<<20 {
		return errors.New("encrypted state payload size is invalid")
	}
	var envelope struct {
		EncryptionVersion json.RawMessage            `json:"encryption_version"`
		EncryptedData     json.RawMessage            `json:"encrypted_data"`
		Meta              map[string]json.RawMessage `json:"meta"`
		Resources         json.RawMessage            `json:"resources"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&envelope); err != nil {
		return errors.New("state payload is not a valid encrypted envelope")
	}
	if len(envelope.EncryptionVersion) == 0 || len(envelope.EncryptedData) == 0 || len(envelope.Meta) == 0 || len(envelope.Resources) != 0 {
		return errors.New("plaintext or incomplete OpenTofu state is forbidden")
	}
	return nil
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
