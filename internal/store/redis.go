package store

import (
	"fmt"
	"sql-proxy/internal/config"

	"github.com/redis/go-redis/v9"
)

var redisClient *redis.Client

func InitRedis(cfg *config.RedisConfig) {
	if cfg == nil {
		return
	}
	host := cfg.Host
	if host == "" {
		return
	}
	redisClient = redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", host, cfg.Port),
		Password: cfg.Password,
		DB:       cfg.DB,
	})
}
