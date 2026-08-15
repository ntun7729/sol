package main

import (
	"context"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

const (
	defaultClientListen = "127.0.0.1:1080"
	defaultDirectListen = "0.0.0.0:1080"
	defaultHTTPPort     = "10000"
	maxTargetLength     = 1024
)

var errUnsupportedSOCKSCommand = errors.New("unsupported SOCKS5 command")

type connector func(context.Context, string) (net.Conn, error)

type credentials struct {
	username string
	password string
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "server":
		err = runServer()
	case "client":
		err = runClient()
	case "direct":
		err = runDirect()
	case "help", "-h", "--help":
		usage()
		return
	default:
		usage()
		err = fmt.Errorf("unknown mode %q", os.Args[1])
	}
	if err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `sol - small SOCKS5-over-WebSocket tunnel

Usage:
  sol server   Render-compatible WebSocket egress server
  sol client   local SOCKS5 proxy tunneled to a sol server
  sol direct   authenticated SOCKS5 server for raw-TCP hosts

Environment:
  server: PORT=10000 SOL_TOKEN=<secret>
  client: SOL_SERVER_URL=wss://host/ws SOL_TOKEN=<secret> [SOL_LISTEN=127.0.0.1:1080]
  direct: SOL_USERNAME=<user> SOL_PASSWORD=<pass> [SOL_LISTEN=0.0.0.0:1080]`)
}

func runServer() error {
	token := os.Getenv("SOL_TOKEN")
	if len(token) < 16 {
		return errors.New("SOL_TOKEN must be at least 16 characters")
	}
	port := envOr("PORT", defaultHTTPPort)
	addr := "0.0.0.0:" + port

	egressDialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "sol tunnel\n")
	})
	mux.Handle("/ws", tunnelHandler(token, egressDialer))
	mux.Handle("/mux", muxTunnelHandler(token, egressDialer))

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("server listening on %s", addr)
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func tunnelHandler(token string, dialer *net.Dialer) http.Handler {
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
		defer ws.CloseNow()

		stream := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
		defer stream.Close()

		target, err := readTarget(stream)
		if err != nil {
			_ = writeStatus(stream, 1)
			return
		}

		dialCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
		pipe(stream, dst)
	})
}

func runClient() error {
	serverURL := os.Getenv("SOL_SERVER_URL")
	if serverURL == "" {
		return errors.New("SOL_SERVER_URL is required")
	}
	if !strings.HasPrefix(serverURL, "wss://") && !strings.HasPrefix(serverURL, "ws://") {
		return errors.New("SOL_SERVER_URL must start with wss:// or ws://")
	}
	token := os.Getenv("SOL_TOKEN")
	if len(token) < 16 {
		return errors.New("SOL_TOKEN must be at least 16 characters")
	}
	listen := envOr("SOL_LISTEN", defaultClientListen)
	if err := requireLoopbackListen(listen); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	log.Printf("SOCKS5 client listening on %s", listen)
	return serveSOCKS(ln, optimizedConnector(serverURL, token), nil)
}

func wsConnector(serverURL, token string) connector {
	httpClient := websocketHTTPClient()
	return func(ctx context.Context, target string) (net.Conn, error) {
		header := http.Header{}
		header.Set("Authorization", "Bearer "+token)
		ws, resp, err := websocket.Dial(ctx, serverURL, &websocket.DialOptions{
			HTTPClient:      httpClient,
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
			stream.Close()
			return nil, err
		}
		var status [1]byte
		if _, err := io.ReadFull(stream, status[:]); err != nil {
			stream.Close()
			return nil, fmt.Errorf("tunnel status: %w", err)
		}
		if status[0] != 0 {
			stream.Close()
			return nil, errors.New("remote server could not connect to target")
		}
		return stream, nil
	}
}

func runDirect() error {
	user := os.Getenv("SOL_USERNAME")
	pass := os.Getenv("SOL_PASSWORD")
	if user == "" || pass == "" {
		return errors.New("direct mode requires SOL_USERNAME and SOL_PASSWORD")
	}
	if len(user) > 255 || len(pass) > 255 {
		return errors.New("SOCKS5 username/password must each be at most 255 bytes")
	}
	listen := envOr("SOL_LISTEN", defaultDirectListen)
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	log.Printf("authenticated SOCKS5 server listening on %s", listen)
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return serveSOCKS(ln, func(ctx context.Context, target string) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp", target)
	}, &credentials{username: user, password: pass})
}

func serveSOCKS(ln net.Listener, connect connector, creds *credentials) error {
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
			if err := handleSOCKS(conn, connect, creds); err != nil && !errors.Is(err, errUnsupportedSOCKSCommand) {
				log.Printf("SOCKS5 connection: %v", err)
			}
		}()
	}
}

func handleSOCKS(client net.Conn, connect connector, creds *credentials) error {
	_ = client.SetDeadline(time.Now().Add(45 * time.Second))
	if err := negotiateSOCKS(client, creds); err != nil {
		return err
	}
	target, err := readSOCKSRequest(client)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
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

func negotiateSOCKS(conn net.Conn, creds *credentials) error {
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

	wanted := byte(0x00)
	if creds != nil {
		wanted = 0x02
	}
	if !containsByte(methods, wanted) {
		_, _ = conn.Write([]byte{0x05, 0xff})
		return errors.New("required authentication method not offered")
	}
	if _, err := conn.Write([]byte{0x05, wanted}); err != nil {
		return err
	}
	if creds != nil {
		return authenticateUserPass(conn, creds)
	}
	return nil
}

func authenticateUserPass(conn net.Conn, creds *credentials) error {
	var head [2]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return err
	}
	if head[0] != 0x01 {
		return errors.New("unsupported username/password auth version")
	}
	user := make([]byte, int(head[1]))
	if _, err := io.ReadFull(conn, user); err != nil {
		return err
	}
	var plen [1]byte
	if _, err := io.ReadFull(conn, plen[:]); err != nil {
		return err
	}
	pass := make([]byte, int(plen[0]))
	if _, err := io.ReadFull(conn, pass); err != nil {
		return err
	}
	okUser := subtle.ConstantTimeCompare(user, []byte(creds.username))
	okPass := subtle.ConstantTimeCompare(pass, []byte(creds.password))
	if okUser&okPass != 1 {
		_, _ = conn.Write([]byte{0x01, 0x01})
		return errors.New("invalid SOCKS5 credentials")
	}
	_, err := conn.Write([]byte{0x01, 0x00})
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

func readTarget(r io.Reader) (string, error) {
	var nbuf [2]byte
	if _, err := io.ReadFull(r, nbuf[:]); err != nil {
		return "", err
	}
	n := int(binary.BigEndian.Uint16(nbuf[:]))
	if n == 0 || n > maxTargetLength {
		return "", errors.New("invalid target length")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	target := string(buf)
	if _, _, err := net.SplitHostPort(target); err != nil {
		return "", fmt.Errorf("invalid target %q: %w", target, err)
	}
	return target, nil
}

func writeStatus(w io.Writer, status byte) error {
	_, err := w.Write([]byte{status})
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

func validBearer(header, token string) bool {
	prefix := "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got := header[len(prefix):]
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func requireLoopbackListen(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid SOL_LISTEN: %w", err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("client mode must listen on a loopback address; use direct mode for a remote SOCKS5 listener")
	}
	return nil
}

func containsByte(values []byte, wanted byte) bool {
	for _, v := range values {
		if v == wanted {
			return true
		}
	}
	return false
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
