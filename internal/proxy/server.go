package proxy

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"sql-proxy/internal/backend"
	"sql-proxy/internal/config"

	. "github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/server"
	"github.com/sirupsen/logrus"
)

type Server struct {
	cfg      *config.MySQLServerConfig
	backend  backend.Client
	listener net.Listener
	connID   uint32
	wg       sync.WaitGroup
	stopped  atomic.Bool
}

func NewServer(cfg *config.MySQLServerConfig, backendClient backend.Client) *Server {
	return &Server{
		cfg:     cfg,
		backend: backendClient,
	}
}

func (s *Server) Start() error {
	var err error
	s.listener, err = net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s failed: %w", s.cfg.Addr, err)
	}

	logrus.Infof("MySQL proxy server listening on %s", s.cfg.Addr)
	logrus.Infof("  User: %s", s.cfg.User)

	for {
		if s.stopped.Load() {
			return nil
		}

		conn, err := s.listener.Accept()
		if err != nil {
			if s.stopped.Load() {
				return nil
			}
			logrus.Errorf("accept connection error: %v", err)
			continue
		}

		s.wg.Add(1)
		go s.handleConnection(conn)
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	connID := atomic.AddUint32(&s.connID, 1)
	remoteAddr := conn.RemoteAddr()

	logrus.Debugf("[conn %d] new connection from %s", connID, remoteAddr)

	handler := NewHandler(s.backend, connID)

	config := server.NewServer(s.cfg.Version, DEFAULT_COLLATION_ID, s.cfg.AuthMethod, nil, nil)

	p := server.NewInMemoryAuthenticationHandler(s.cfg.AuthMethod)
	if err := p.AddUser(s.cfg.User, s.cfg.Password); err != nil {
		logrus.Errorf("[conn %d] add user error: %v", connID, err)
		return
	}

	connHandler, err := config.NewCustomizedConn(conn, p, handler)
	if err != nil {
		if isExpectedProbeError(remoteAddr.String(), err) {
			logrus.Debugf("[conn %d] client disconnected before MySQL handshake: %v", connID, err)
		} else {
			logrus.Warnf("[conn %d] create connection handler error: %v", connID, err)
		}
		return
	}

	attrs := connHandler.Attributes()

	handler.setAttributes(attrs)

	logrus.Infof("[conn %d] authenticated: user=%s from %s", connID, s.cfg.User, remoteAddr)

	for {
		if s.stopped.Load() {
			return
		}

		if err := connHandler.HandleCommand(); err != nil {
			if s.stopped.Load() {
				return
			}
			logrus.Debugf("[conn %d] connection closed: %v", connID, err)
			return
		}
	}
}

func isExpectedProbeError(address string, err error) bool {
	if strings.HasPrefix(address, "127.0.0.1") {
		return true
	}
	return strings.Contains(err.Error(), "err EOF") || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE)
}

func (s *Server) Stop() error {
	s.stopped.Store(true)
	if s.listener != nil {
		s.listener.Close()
	}
	s.wg.Wait()
	logrus.Info("MySQL proxy server stopped")
	return nil
}
