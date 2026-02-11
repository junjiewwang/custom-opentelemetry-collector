// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package blobstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/tencentyun/cos-go-sdk-v5"
	"go.uber.org/zap"
)

// cosBlobStore implements BlobStore using Tencent Cloud Object Storage (COS).
//
// Storage layout:
//
//	<key_prefix><key>.blob     — binary data
//	<key_prefix><key>.meta.json — metadata JSON
type cosBlobStore struct {
	logger    *zap.Logger
	client    *cos.Client
	cfg       Config
	keyPrefix string
}

// NewCOSBlobStore creates a COS-backed BlobStore.
func NewCOSBlobStore(logger *zap.Logger, cfg Config) (BlobStore, error) {
	cosCfg := cfg.COS
	if cosCfg.Bucket == "" || cosCfg.Region == "" {
		return nil, fmt.Errorf("cos bucket and region are required")
	}
	if cosCfg.SecretID == "" || cosCfg.SecretKey == "" {
		return nil, fmt.Errorf("cos secret_id and secret_key are required")
	}

	scheme := cosCfg.Scheme
	if scheme == "" {
		scheme = "https"
	}
	domain := cosCfg.Domain
	if domain == "" {
		domain = "myqcloud.com"
	}

	bucketURL, err := url.Parse(fmt.Sprintf("%s://%s.cos.%s.%s", scheme, cosCfg.Bucket, cosCfg.Region, domain))
	if err != nil {
		return nil, fmt.Errorf("failed to parse COS bucket URL: %w", err)
	}
	serviceURL, err := url.Parse(fmt.Sprintf("%s://cos.%s.%s", scheme, cosCfg.Region, domain))
	if err != nil {
		return nil, fmt.Errorf("failed to parse COS service URL: %w", err)
	}

	client := cos.NewClient(&cos.BaseURL{
		BucketURL:  bucketURL,
		ServiceURL: serviceURL,
	}, &http.Client{
		Transport: &cos.AuthorizationTransport{
			SecretID:  cosCfg.SecretID,
			SecretKey: cosCfg.SecretKey,
		},
	})

	s := &cosBlobStore{
		logger:    logger,
		client:    client,
		cfg:       cfg,
		keyPrefix: cosCfg.KeyPrefix,
	}

	logger.Info("COS blob store initialized",
		zap.String("bucket", cosCfg.Bucket),
		zap.String("region", cosCfg.Region),
		zap.String("key_prefix", cosCfg.KeyPrefix),
		zap.Int64("max_blob_size", cfg.MaxBlobSize),
	)

	return s, nil
}

// objectKey returns the full COS object key with prefix and suffix.
func (s *cosBlobStore) objectKey(key, suffix string) string {
	return s.keyPrefix + key + suffix
}

// blobObjectKey returns the COS object key for the blob data.
// If the key already has a file extension, it is used as-is (no ".blob" suffix).
func (s *cosBlobStore) blobObjectKey(key string) string {
	return s.keyPrefix + key + blobDataSuffix(key)
}

// metaObjectKey returns the COS object key for the metadata file.
func (s *cosBlobStore) metaObjectKey(key string) string {
	return s.keyPrefix + key + ".meta.json"
}

// Put implements BlobStore.
func (s *cosBlobStore) Put(ctx context.Context, key string, reader io.Reader, metadata map[string]string) (int64, error) {
	var dataReader io.Reader = reader
	var written int64

	if s.cfg.MaxBlobSize > 0 {
		// Enforce max size: read into a limited buffer, then upload.
		limitedReader := io.LimitReader(reader, s.cfg.MaxBlobSize+1)
		data, err := io.ReadAll(limitedReader)
		if err != nil {
			return 0, fmt.Errorf("failed to read blob data: %w", err)
		}
		if int64(len(data)) > s.cfg.MaxBlobSize {
			return 0, ErrTooLarge
		}
		written = int64(len(data))
		dataReader = bytes.NewReader(data)
	}

	// Upload blob data
	blobKey := s.blobObjectKey(key)
	_, err := s.client.Object.Put(ctx, blobKey, dataReader, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to upload blob to COS %q: %w", blobKey, err)
	}

	// If we didn't pre-read (no size limit), get the size from COS head
	if s.cfg.MaxBlobSize <= 0 {
		resp, err := s.client.Object.Head(ctx, blobKey, nil)
		if err == nil {
			written = resp.ContentLength
		}
	}

	// Upload metadata
	meta := cosMeta{
		Key:         key,
		Size:        written,
		ContentType: metadata["content_type"],
		Metadata:    metadata,
		CreatedAt:   time.Now(),
	}
	metaData, err := json.Marshal(meta)
	if err != nil {
		return written, fmt.Errorf("failed to marshal metadata: %w", err)
	}

	metaKey := s.metaObjectKey(key)
	_, err = s.client.Object.Put(ctx, metaKey, bytes.NewReader(metaData), nil)
	if err != nil {
		return written, fmt.Errorf("failed to upload metadata to COS %q: %w", metaKey, err)
	}

	s.logger.Debug("Blob stored to COS",
		zap.String("key", key),
		zap.Int64("size", written),
	)

	return written, nil
}

// Get implements BlobStore.
func (s *cosBlobStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	blobKey := s.blobObjectKey(key)

	resp, err := s.client.Object.Get(ctx, blobKey, nil)
	if err != nil {
		if isCOSNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get blob from COS %q: %w", blobKey, err)
	}

	return resp.Body, nil
}

// GetMeta implements BlobStore.
func (s *cosBlobStore) GetMeta(ctx context.Context, key string) (*BlobMeta, error) {
	metaKey := s.metaObjectKey(key)

	resp, err := s.client.Object.Get(ctx, metaKey, nil)
	if err != nil {
		if isCOSNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get metadata from COS %q: %w", metaKey, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata from COS %q: %w", metaKey, err)
	}

	var meta cosMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata %q: %w", metaKey, err)
	}

	return &BlobMeta{
		Key:         meta.Key,
		Size:        meta.Size,
		ContentType: meta.ContentType,
		Metadata:    meta.Metadata,
		CreatedAt:   meta.CreatedAt,
	}, nil
}

// Delete implements BlobStore.
func (s *cosBlobStore) Delete(ctx context.Context, key string) error {
	blobKey := s.blobObjectKey(key)
	metaKey := s.metaObjectKey(key)

	// Delete metadata first (best effort)
	_, _ = s.client.Object.Delete(ctx, metaKey)

	// Delete blob
	_, err := s.client.Object.Delete(ctx, blobKey)
	if err != nil && !isCOSNotFound(err) {
		return fmt.Errorf("failed to delete blob from COS %q: %w", blobKey, err)
	}

	return nil
}

// Close implements BlobStore.
func (s *cosBlobStore) Close() error {
	// COS client does not hold persistent connections that need explicit cleanup.
	return nil
}

// cosMeta is the metadata format stored alongside blobs in COS.
type cosMeta struct {
	Key         string            `json:"key"`
	Size        int64             `json:"size"`
	ContentType string            `json:"content_type,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
}

// isCOSNotFound checks if a COS error indicates the object does not exist.
func isCOSNotFound(err error) bool {
	if err == nil {
		return false
	}
	if cosErr, ok := cos.IsCOSError(err); ok {
		return cosErr.Code == "NoSuchKey" || cosErr.Response != nil && cosErr.Response.StatusCode == http.StatusNotFound
	}
	return false
}
