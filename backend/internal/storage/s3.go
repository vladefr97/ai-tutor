// Package storage keeps immutable course versions in S3-compatible storage.
package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Store struct {
	c      *minio.Client
	Bucket string
}

// New connects and creates the bucket if needed. endpoint is host:port.
func New(ctx context.Context, endpoint, accessKey, secretKey, bucket string, secure bool) (*Store, error) {
	c, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Secure: secure})
	if err != nil {
		return nil, err
	}
	ok, err := c.BucketExists(ctx, bucket)
	if err != nil {
		return nil, err
	}
	if !ok {
		if err := c.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, err
		}
	}
	return &Store{c: c, Bucket: bucket}, nil
}

func (s *Store) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.c.PutObject(ctx, s.Bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{})
	return err
}

func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	obj, err := s.c.GetObject(ctx, s.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(obj)
}

// PutDir uploads every regular file under dir to prefix/<relative path>.
func (s *Store) PutDir(ctx context.Context, prefix, dir string) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return s.Put(ctx, prefix+"/"+filepath.ToSlash(rel), data)
	})
}

// GetPrefix downloads every object under prefix into dir, keeping relative paths.
func (s *Store) GetPrefix(ctx context.Context, prefix, dir string) error {
	prefix = strings.TrimSuffix(prefix, "/") + "/"
	for obj := range s.c.ListObjects(ctx, s.Bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if obj.Err != nil {
			return obj.Err
		}
		rel := strings.TrimPrefix(obj.Key, prefix)
		if rel == "" || strings.Contains(rel, "..") {
			continue
		}
		data, err := s.Get(ctx, obj.Key)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
