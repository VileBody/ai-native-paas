package remotestate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type S3Config struct {
	Endpoint  string
	Region    string
	Bucket    string
	Prefix    string
	AccessKey string
	SecretKey string
}

type S3BlobStore struct {
	client *minio.Client
	bucket string
	prefix string
}

func NewS3BlobStore(config S3Config) (*S3BlobStore, error) {
	parsed, err := url.Parse(config.Endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, errors.New("valid S3 endpoint URL is required")
	}
	if config.Bucket == "" || config.AccessKey == "" || config.SecretKey == "" {
		return nil, errors.New("complete S3 configuration is required")
	}
	client, err := minio.New(parsed.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(config.AccessKey, config.SecretKey, ""),
		Secure:       parsed.Scheme == "https",
		Region:       config.Region,
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, fmt.Errorf("create S3 client: %w", err)
	}
	return &S3BlobStore{client: client, bucket: config.Bucket, prefix: strings.Trim(config.Prefix, "/")}, nil
}

func (s *S3BlobStore) objectKey(namespace string) (string, error) {
	if err := ValidateNamespace(namespace); err != nil {
		return "", err
	}
	return path.Join(s.prefix, namespace, "terraform.tfstate"), nil
}

func (s *S3BlobStore) Get(ctx context.Context, namespace string) (Blob, error) {
	key, err := s.objectKey(namespace)
	if err != nil {
		return Blob{}, err
	}
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return Blob{}, mapS3Error(err)
	}
	defer object.Close()
	stat, err := object.Stat()
	if err != nil {
		return Blob{}, mapS3Error(err)
	}
	data, err := io.ReadAll(io.LimitReader(object, (64<<20)+1))
	if err != nil {
		return Blob{}, mapS3Error(err)
	}
	if len(data) > 64<<20 {
		return Blob{}, errors.New("stored state exceeds size limit")
	}
	return Blob{Data: data, VersionID: stat.VersionID, ETag: stat.ETag}, nil
}

func (s *S3BlobStore) Put(ctx context.Context, namespace string, data []byte) (Blob, error) {
	key, err := s.objectKey(namespace)
	if err != nil {
		return Blob{}, err
	}
	info, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: "application/json"})
	if err != nil {
		return Blob{}, mapS3Error(err)
	}
	return Blob{Data: append([]byte(nil), data...), VersionID: info.VersionID, ETag: info.ETag}, nil
}

func (s *S3BlobStore) Delete(ctx context.Context, namespace string) error {
	key, err := s.objectKey(namespace)
	if err != nil {
		return err
	}
	return mapS3Error(s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}))
}

func mapS3Error(err error) error {
	if err == nil {
		return nil
	}
	response := minio.ToErrorResponse(err)
	if response.Code == "NoSuchKey" || response.Code == "NoSuchObject" || response.StatusCode == 404 {
		return ErrNotFound
	}
	return err
}
