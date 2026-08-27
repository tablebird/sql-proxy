package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"sql-proxy/internal/backend"
	"sql-proxy/internal/config"
	"sql-proxy/internal/proxy"

	"github.com/sirupsen/logrus"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	if err := config.Load(*configPath); err != nil {
		logrus.Fatalf("load config failed: %v", err)
	}

	logrus.Info("starting sql-proxy...")

	backendClient := backend.NewClient(&config.AppConfig.Backend)

	srv := proxy.NewServer(&config.AppConfig.MySQLServer, backendClient)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if err != nil {
			logrus.Fatalf("server error: %v", err)
		}
	case sig := <-sigCh:
		logrus.Infof("received signal: %v, shutting down...", sig)
		if err := srv.Stop(); err != nil {
			logrus.Errorf("stop server error: %v", err)
		}
	}

	logrus.Info("sql-proxy stopped")
}
