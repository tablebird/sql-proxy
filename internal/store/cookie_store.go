package store

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

type CookieStore interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, expiration time.Duration) error
	Delete(ctx context.Context, key string) error
}

func NewRedisCookieStore() CookieStore {
	if redisClient == nil {
		return nil
	}
	return &redisCookieStore{redisClient}
}

type redisCookieStore struct {
	client *redis.Client
}

func (s *redisCookieStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	value, err := s.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return value, true, nil
}

func (s *redisCookieStore) Set(ctx context.Context, key string, value []byte, expiration time.Duration) error {
	logrus.Debugf("cookie redis set key : %s", key)
	return s.client.Set(ctx, key, value, expiration).Err()
}

func (s *redisCookieStore) Delete(ctx context.Context, key string) error {
	logrus.Debugf("cookie redis delete key : %s", key)
	return s.client.Del(ctx, key).Err()
}
