package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

const (
	muxKeepAliveInterval = 20 * time.Second
	muxWriteTimeout      = 45 * time.Second
	muxOpenTimeout       = 20 * time.Second
	muxCloseTimeout      = 5 * time.Second
	muxStreamWindow      = 4 * 1024 * 1024
)

var errMuxUnavailable = errors.New("multiplexed tunnel endpoint unavailable")

type muxClient struct {
	serverURL  string
	token      string
	httpClient *http.Client

	mu      sync.Mutex
	session *yamux.Session
}

func optimizedConnector(serverURL, token string) connector {
	muxURL, err := optimizedMuxURL(serverURL)
	if err != nil {
		return wsConnector(serverURL, token)
	}
	mc := &muxClient{
		serverURL:  muxURL,
		token:      token,
		httpClient: websocketHTTPClient(),
	}
	legacy := wsConnector(serverURL, token)
	return func(ctx context.Context, target string) (net.Conn, error) {
		conn, err := mc.connect(ctx, target)
		if err == nil {
			return conn, nil
		}
		if errors.Is(err, errMuxUnavailable) {
			return legacy(ctx, target)
		}
		return nil, err
	}
}

func optimizedMuxURL(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return "", errors.New("unsupported WebSocket scheme")
	}
	path := strings.TrimSuffix(u.Path, "/")
	switch {
	case path == "", path == "/ws":
		u.Path = "/mux"
	case strings.HasSuffix(path, "/ws"):
		u.Path = strings.TrimSuffix(path, "/ws") + "/mux"
	case strings.HasSuffix(path, "/mux"):
		u.Path = path
	default:
		u.Path = path + "/mux"
	}
	return u.String(), nil
}

func websocketHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{
		Timeout:       20 * time.Second,
		KeepAlive:     30 * time.Second,
		FallbackDelay: 200 * time.Millisecond,
	}).DialContext
	transport.ForceAttemptHTTP2 = false
	transport.TLSHandshakeTimeout = 30 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.IdleConnTimeout = 90 * time.Second
	transport.MaxIdleConns = 16
	transport.MaxIdleConnsPerHost = 4

	tlsConfig := transport.TLSClientConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	} else {
		tlsConfig = tlsConfig.Clone()
	}
	tlsConfig.MinVersion = tls.VersionTLS12
	tlsConfig.ClientSessionCache = tls.NewLRUClientSessionCache(64)
	transport.TLSClientConfig = tlsConfig

	return &http.Client{Transport: transport}
}

func tunedYamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.AcceptBacklog = 256
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = muxKeepAliveInterval
	cfg.ConnectionWriteTimeout = muxWriteTimeout
	cfg.MaxStreamWindowSize = muxStreamWindow
	cfg.StreamOpenTimeout = muxOpenTimeout
	cfg.StreamCloseTimeout = muxCloseTimeout
	cfg.LogOutput = io.Discard
	return cfg
}

func (m *muxClient) connect(ctx context.Context, target string) (net.Conn, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		session, err := m.getSession(ctx)
		if err != nil {
			return nil, err
		}
		stream, err := session.Open()
		if err != nil {
			lastErr = err
			m.dropSession(session)
			continue
		}

		if deadline, ok := ctx.Deadline(); ok {
			_ = stream.SetDeadline(deadline)
		}
		if err := writeTarget(stream, target); err != nil {
			_ = stream.Close()
			lastErr = err
			continue
		}
		var status [1]byte
		if _, err := io.ReadFull(stream, status[:]); err != nil {
			_ = stream.Close()
			lastErr = fmt.Errorf("multiplexed tunnel status: %w", err)
			continue
		}
		if status[0] != 0 {
			_ = stream.Close()
			return nil, errors.New("remote server could not connect to target")
		}
		_ = stream.SetDeadline(time.Time{})
		return stream, nil
	}
	return nil, fmt.Errorf("multiplexed tunnel failed: %w", lastErr)
}

func (m *muxClient) getSession(ctx context.Context) (*yamux.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.session != nil && !m.session.IsClosed() {
		return m.session, nil
	}

	header := http.Header{}
	header.Set("Authorization", "Bearer "+m.token)
	ws, resp, err := websocket.Dial(ctx, m.serverURL, &websocket.DialOptions{
		HTTPClient:      m.httpClient,
		HTTPHeader:      header,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if resp != nil {
			if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusUpgradeRequired {
				return nil, fmt.Errorf("%w: HTTP %d", errMuxUnavailable, resp.StatusCode)
			}
			return nil, fmt.Errorf("multiplexed websocket dial failed with HTTP %d: %w", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("multiplexed websocket dial failed: %w", err)
	}

	stream := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	session, err := yamux.Client(stream, tunedYamuxConfig())
	if err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("start multiplexed session: %w", err)
	}
	m.session = session
	log.Printf("optimized multiplexed tunnel connected to %s", m.serverURL)
	return session, nil
}

func (m *muxClient) dropSession(session *yamux.Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session == session {
		_ = m.session.Close()
		m.session = nil
	}
}

func muxTunnelHandler(token string, dialer *net.Dialer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validBearer(r.Header.Get("Authorization"), token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionDisabled,
		})
		if err != nil {
			return
		}
		stream := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
		defer stream.Close()

		session, err := yamux.Server(stream, tunedYamuxConfig())
		if err != nil {
			return
		}
		defer session.Close()

		for {
			logical, err := session.Accept()
			if err != nil {
				return
			}
			go serveMuxStream(logical, dialer)
		}
	})
}

func serveMuxStream(stream net.Conn, dialer *net.Dialer) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(30 * time.Second))

	target, err := readTarget(stream)
	if err != nil {
		_ = writeStatus(stream, 1)
		return
	}

	dialCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	dst, err := dialer.DialContext(dialCtx, "tcp", target)
	cancel()
	if err != nil {
		_ = writeStatus(stream, 1)
		return
	}
	defer dst.Close()

	if err := writeStatus(stream, 0); err != nil {
		return
	}
	_ = stream.SetDeadline(time.Time{})
	pipe(stream, dst)
}
