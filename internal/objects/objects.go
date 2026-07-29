// Package objects wraps the S3-compatible blob store (Garage today).
//
// Everything goes through this interface so the archive tier, and any future
// offsite target, can be swapped in without touching call sites.
package objects

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Store struct {
	c      *minio.Client
	bucket string
}

func Open(ctx context.Context, endpoint, keyID, secret, region, bucket string, useSSL bool) (*Store, error) {
	c, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(keyID, secret, ""),
		Secure: useSSL,
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 client: %w", err)
	}
	ok, err := c.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("bucket check: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("bucket %q does not exist", bucket)
	}
	return &Store{c: c, bucket: bucket}, nil
}

func (s *Store) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.c.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	return err
}

func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	o, err := s.c.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer o.Close()
	return io.ReadAll(o)
}

func (s *Store) Has(ctx context.Context, key string) (bool, error) {
	_, err := s.c.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}
	var e minio.ErrorResponse
	if errors.As(err, &e) && (e.Code == "NoSuchKey" || e.StatusCode == 404) {
		return false, nil
	}
	return false, err
}

func (s *Store) Delete(ctx context.Context, key string) error {
	return s.c.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}
