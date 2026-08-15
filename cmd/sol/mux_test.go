package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOptimizedMuxURL(t *testing.T) {
	cases := map[string]string{
		"wss://example.com/ws":          "wss://example.com/mux",
		"wss://example.com/base/ws":     "wss://example.com/base/mux",
		"wss://example.com/mux":         "wss://example.com/mux",
		"wss://example.com/custom/path": "wss://example.com/custom/path/mux",
	}
	for input, want := range cases {
		got, err := optimizedMuxURL(input)
		if err != nil {
			t.Fatalf("optimizedMuxURL(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("optimizedMuxURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMultiplexedConnectorReusesWebSocket(t *testing.T) {
	token := "0123456789abcdef0123456789abcdef"
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()

	dialer := &net.Dialer{Timeout: 2 * time.Second}
	muxHandler := muxTunnelHandler(token, dialer)
	var sessions atomic.Int32
	root := http.NewServeMux()
	root.Handle("/mux", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessions.Add(1)
		muxHandler.ServeHTTP(w, r)
	}))
	ts := httptest.NewServer(root)
	defer ts.Close()

	serverURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/mux"
	mc := &muxClient{serverURL: serverURL, token: token, httpClient: websocketHTTPClient()}
	defer func() {
		mc.mu.Lock()
		if mc.session != nil {
			_ = mc.session.Close()
		}
		mc.mu.Unlock()
	}()

	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, err := mc.connect(ctx, echoLn.Addr().String())
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte("mux reuse")
		if _, err := conn.Write(payload); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, got); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		if string(got) != string(payload) {
			conn.Close()
			t.Fatalf("payload mismatch: got %q want %q", got, payload)
		}
		_ = conn.Close()
	}

	if got := sessions.Load(); got != 1 {
		t.Fatalf("expected one WebSocket session for two streams, got %d", got)
	}
}

func TestMultiplexedTunnelRejectsBadToken(t *testing.T) {
	token := "0123456789abcdef0123456789abcdef"
	ts := httptest.NewServer(muxTunnelHandler(token, &net.Dialer{Timeout: time.Second}))
	defer ts.Close()

	mc := &muxClient{
		serverURL:  "ws" + strings.TrimPrefix(ts.URL, "http"),
		token:      "wrong-wrong-wrong-wrong",
		httpClient: websocketHTTPClient(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := mc.connect(ctx, "127.0.0.1:1"); err == nil {
		t.Fatal("expected authentication failure")
	}
}

func TestUnsupportedSOCKSCommandIsQuietSentinel(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	done := make(chan error, 1)
	go func() {
		_, err := readSOCKSRequest(server)
		done <- err
	}()

	if _, err := client.Write([]byte{0x05, 0x03, 0x00, 0x01}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0x07 {
		t.Fatalf("unexpected SOCKS reply: %v", reply)
	}
	if err := <-done; !errors.Is(err, errUnsupportedSOCKSCommand) {
		t.Fatalf("expected unsupported command sentinel, got %v", err)
	}
}
