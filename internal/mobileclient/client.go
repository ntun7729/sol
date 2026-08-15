package mobileclient

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

const (
	maxTargetLength       = 1024
	muxKeepAliveInterval  = 20 * time.Second
	muxWriteTimeout       = 45 * time.Second
	muxOpenTimeout        = 20 * time.Second
	muxCloseTimeout       = 5 * time.Second
	muxStreamWindow       = 4 * 1024 * 1024
	defaultConnectTimeout = 30 * time.Second
)

var errUnsupportedSOCKSCommand = errors.New("unsupported SOCKS5 command")

type connector func(context.Context, string) (net.Conn, error)

// Client runs a loopback SOCKS5 listener backed by a persistent SOL WebSocket
// session. It is intentionally TCP CONNECT-only; Android UDP traffic is expected
// to fall back to TCP while DNS is handled by the TUN adapter's mapped-DNS mode.
type Client struct {
	ln   net.Listener
	mux  *muxClient
	once sync.Once
	done chan struct{}
}

// Start starts a SOL SOCKS5 client and returns after the local listener is ready.
func Start(serverURL, token, listen string) (*Client, error) {
	if !strings.HasPrefix(serverURL, "wss://") && !strings.HasPrefix(serverURL, "ws://") {
		return nil, errors.New("server URL must start with wss:// or ws://")
	}
	if len(token) < 16 {
		return nil, errors.New("token must be at least 16 characters")
	}
	if listen == "" {
		listen = "127.0.0.1:1080"
	}
	if err := requireLoopbackListen(listen); err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, err
	}

	mc, err := newMuxClient(serverURL, token)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	legacy := wsConnector(serverURL, token)
	connect := func(ctx context.Context, target string) (net.Conn, error) {
		conn, err := mc.connect(ctx, target)
		if err == nil {
			return conn, nil
		}
		if errors.Is(err, errMuxUnavailable) {
			return legacy(ctx, target)
		}
		return nil, err
	}

	c := &Client{ln: ln, mux: mc, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		if err := serveSOCKS(ln, connect); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Printf("mobile SOCKS5 listener stopped: %v", err)
		}
	}()
	log.Printf("mobile SOCKS5 client listening on %s", listen)
	return c, nil
}

// Close stops the SOCKS listener and the persistent mux session.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.once.Do(func() {
		_ = c.ln.Close()
		c.mux.close()
		select {
		case <-c.done:
		case <-time.After(2 * time.Second):
		}
	})
}

var errMuxUnavailable = errors.New("multiplexed tunnel endpoint unavailable")

type muxClient struct {
	serverURL  string
	token      string
	httpClient *http.Client

	mu      sync.Mutex
	session *yamux.Session
}

func newMuxClient(serverURL, token string) (*muxClient, error) {
	muxURL, err := optimizedMuxURL(serverURL)
	if err != nil {
		return nil, err
	}
	return &muxClient{
		serverURL:  muxURL,
		token:      token,
		httpClient: websocketHTTPClient(),
	}, nil
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
		FallbackDelay: 250 * time.Millisecond,
	}).DialContext
	transport.ForceAttemptHTTP2 = false
	transport.TLSHandshakeTimeout = 30 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.IdleConnTimeout = 90 * time.Second
	transport.MaxIdleConnsPerHost = 4
	transport.TLSClientConfig = &tls.Config{ClientSessionCache: tls.NewLRUClientSessionCache(32)}
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

func (m *muxClient) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session != nil {
		_ = m.session.Close()
		m.session = nil
	}
}

func wsConnector(serverURL, token string) connector {
	client := websocketHTTPClient()
	return func(ctx context.Context, target string) (net.Conn, error) {
		header := http.Header{}
		header.Set("Authorization", "Bearer "+token)
		ws, resp, err := websocket.Dial(ctx, serverURL, &websocket.DialOptions{
			HTTPClient:      client,
			HTTPHeader:      header,
			CompressionMode: websocket.CompressionDisabled,
		})
		if err != nil {
			if resp != nil {
				return nil, fmt.Errorf("websocket dial failed with HTTP %d: %w", resp.StatusCode, err)
			}
			return nil, fmt.Errorf("websocket dial failed: %w", err)
		}
		stream := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
		if err := writeTarget(stream, target); err != nil {
			_ = stream.Close()
			return nil, err
		}
		var status [1]byte
		if _, err := io.ReadFull(stream, status[:]); err != nil {
			_ = stream.Close()
			return nil, fmt.Errorf("tunnel status: %w", err)
		}
		if status[0] != 0 {
			_ = stream.Close()
			return nil, errors.New("remote server could not connect to target")
		}
		return stream, nil
	}
}

func serveSOCKS(ln net.Listener, connect connector) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go func() {
			defer conn.Close()
			if err := handleSOCKS(conn, connect); err != nil && !errors.Is(err, errUnsupportedSOCKSCommand) {
				log.Printf("SOCKS5 connection: %v", err)
			}
		}()
	}
}

func handleSOCKS(client net.Conn, connect connector) error {
	_ = client.SetDeadline(time.Now().Add(defaultConnectTimeout))
	if err := negotiateSOCKS(client); err != nil {
		return err
	}
	target, err := readSOCKSRequest(client)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultConnectTimeout)
	defer cancel()
	remote, err := connect(ctx, target)
	if err != nil {
		_ = writeSOCKSReply(client, 0x05)
		return err
	}
	defer remote.Close()
	if err := writeSOCKSReply(client, 0x00); err != nil {
		return err
	}
	_ = client.SetDeadline(time.Time{})
	pipe(client, remote)
	return nil
}

func negotiateSOCKS(conn net.Conn) error {
	var hdr [2]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return err
	}
	if hdr[0] != 0x05 {
		return errors.New("unsupported SOCKS version")
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	if !containsByte(methods, 0x00) {
		_, _ = conn.Write([]byte{0x05, 0xff})
		return errors.New("SOCKS5 no-auth method not offered")
	}
	_, err := conn.Write([]byte{0x05, 0x00})
	return err
}

func readSOCKSRequest(conn net.Conn) (string, error) {
	var req [4]byte
	if _, err := io.ReadFull(conn, req[:]); err != nil {
		return "", err
	}
	if req[0] != 0x05 {
		return "", errors.New("unsupported SOCKS version")
	}
	if req[1] != 0x01 {
		_ = writeSOCKSReply(conn, 0x07)
		return "", errUnsupportedSOCKSCommand
	}

	var host string
	switch req[3] {
	case 0x01:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		host = net.IP(buf).String()
	case 0x03:
		var n [1]byte
		if _, err := io.ReadFull(conn, n[:]); err != nil {
			return "", err
		}
		if n[0] == 0 {
			return "", errors.New("empty domain name")
		}
		buf := make([]byte, int(n[0]))
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		host = string(buf)
	case 0x04:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		host = net.IP(buf).String()
	default:
		_ = writeSOCKSReply(conn, 0x08)
		return "", errors.New("unsupported SOCKS5 address type")
	}

	var p [2]byte
	if _, err := io.ReadFull(conn, p[:]); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(p[:])
	if port == 0 {
		return "", errors.New("invalid target port 0")
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

func writeSOCKSReply(conn net.Conn, code byte) error {
	_, err := conn.Write([]byte{0x05, code, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	return err
}

func writeTarget(w io.Writer, target string) error {
	if target == "" || len(target) > maxTargetLength {
		return errors.New("invalid target length")
	}
	buf := make([]byte, 2+len(target))
	binary.BigEndian.PutUint16(buf[:2], uint16(len(target)))
	copy(buf[2:], target)
	_, err := w.Write(buf)
	return err
}

func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	copyOne := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if tcp, ok := dst.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}
	go copyOne(a, b)
	go copyOne(b, a)
	<-done
}

func containsByte(values []byte, wanted byte) bool {
	for _, v := range values {
		if v == wanted {
			return true
		}
	}
	return false
}

func requireLoopbackListen(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("mobile client must listen on a loopback address")
	}
	return nil
}
