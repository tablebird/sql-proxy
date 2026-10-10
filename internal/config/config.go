package config

import (
	"fmt"
	"os"
	"sql-proxy/internal/log"
	"time"

	. "github.com/go-mysql-org/go-mysql/mysql"

	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

type Config struct {
	MySQLServer MySQLServerConfig `mapstructure:"mysql_server"`
	Backend     BackendConfig     `mapstructure:"backend"`
	Redis       RedisConfig       `mapstructure:"redis"`
	Log         LogConfig         `mapstructure:"log"`
}

type MySQLServerConfig struct {
	Addr       string `mapstructure:"addr"`
	Version    string `mapstructure:"version"`
	AuthMethod string `mapstructure:"auth_method"`
	User       string `mapstructure:"user"`
	Password   string `mapstructure:"password"`
}

type BackendConfig struct {
	BaseURL           string            `mapstructure:"base_url"`
	Username          string            `mapstructure:"username"`
	Password          string            `mapstructure:"password"`
	Timeout           int               `mapstructure:"timeout"`
	Headers           map[string]string `mapstructure:"headers"`
	MaxRetries        int               `mapstructure:"max_retries"`
	ProxyUrl          string            `mapstructure:"proxy_url"`
	AuthReuseInterval time.Duration     `mapstructure:"auth_reuse_interval"`

	DeprecatedURL string `mapstructure:"url"`
	DeprecatedKey string `mapstructure:"api_key"`
}

type RedisConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

var AppConfig *Config

func Load(path string) error {
	viper.SetConfigFile(path)
	viper.SetConfigType("yaml")

	viper.SetDefault("mysql_server.addr", "127.0.0.1:3306")
	viper.SetDefault("mysql_server.version", "5.7.0")
	viper.SetDefault("mysql_server.auth_method", AUTH_NATIVE_PASSWORD)
	viper.SetDefault("mysql_server.user", "root")
	viper.SetDefault("mysql_server.password", "")

	viper.SetDefault("backend.base_url", "https://sqlpara.example.com")
	viper.SetDefault("backend.auth_reuse_interval", 30*time.Minute)
	viper.SetDefault("backend.password", "")
	viper.SetDefault("backend.timeout", 30)
	viper.SetDefault("backend.max_retries", 3)

	viper.SetDefault("log.level", "info")
	viper.SetDefault("log.format", "text")

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(*os.PathError); !ok {
			return fmt.Errorf("read config error: %w", err)
		}
		logrus.Warn("config file not found, using defaults")
	}

	AppConfig = &Config{}
	if err := viper.Unmarshal(AppConfig); err != nil {
		return fmt.Errorf("unmarshal config error: %w", err)
	}

	if AppConfig.Backend.BaseURL == "" && AppConfig.Backend.DeprecatedURL != "" {
		AppConfig.Backend.BaseURL = AppConfig.Backend.DeprecatedURL
	}

	if err := setupLog(); err != nil {
		return err
	}

	return nil
}

func setupLog() error {
	level, err := logrus.ParseLevel(AppConfig.Log.Level)
	if err != nil {
		return fmt.Errorf("parse log level error: %w", err)
	}
	logrus.SetLevel(level)

	switch AppConfig.Log.Format {
	case "json":
		logrus.SetFormatter(&logrus.JSONFormatter{})
	default:
		logrus.SetFormatter(&log.CustomFormatter{
			StartTime: time.Now(),
			TextFormatter: logrus.TextFormatter{
				DisableColors: false, // 是否关闭颜色
				FullTimestamp: false,
			},
		})
	}

	return nil
}
