package main

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEndToEndSOCKSOverWebSocket(t *testing.T) {
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

	ts := httptest.NewServer(tunnelHandler(token, &net.Dialer{Timeout: 2 * time.Second}))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	socksLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer socksLn.Close()
	go func() {
		_ = serveSOCKS(socksLn, wsConnector(wsURL, token), nil)
	}()

	c, err := net.DialTimeout("tcp", socksLn.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}
	var method [2]byte
	if _, err := io.ReadFull(c, method[:]); err != nil {
		t.Fatal(err)
	}
	if method != [2]byte{0x05, 0x00} {
		t.Fatalf("unexpected method response: %v", method)
	}

	host, portText, err := net.SplitHostPort(echoLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		t.Fatalf("expected IPv4 echo listener, got %q", host)
	}
	port, err := net.LookupPort("tcp", portText)
	if err != nil {
		t.Fatal(err)
	}
	req := []byte{0x05, 0x01, 0x00, 0x01, ip[0], ip[1], ip[2], ip[3], 0, 0}
	binary.BigEndian.PutUint16(req[len(req)-2:], uint16(port))
	if _, err := c.Write(req); err != nil {
		t.Fatal(err)
	}

	reply := make([]byte, 10)
	if _, err := io.ReadFull(c, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0x00 {
		t.Fatalf("SOCKS CONNECT failed: %v", reply)
	}

	payload := []byte("hello through sol")
	if _, err := c.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("payload mismatch: got %q want %q", got, payload)
	}
}

func TestTunnelRejectsBadToken(t *testing.T) {
	h := tunnelHandler("0123456789abcdef", &net.Dialer{Timeout: time.Second})
	ts := httptest.NewServer(h)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connect := wsConnector("ws"+strings.TrimPrefix(ts.URL, "http"), "wrong-wrong-wrong-wrong")
	if _, err := connect(ctx, "127.0.0.1:1"); err == nil {
		t.Fatal("expected authentication failure")
	}
}

func TestDirectSOCKSUsernamePassword(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	done := make(chan error, 1)
	go func() {
		done <- negotiateSOCKS(server, &credentials{username: "u", password: "p"})
	}()

	if _, err := client.Write([]byte{0x05, 0x01, 0x02}); err != nil {
		t.Fatal(err)
	}
	var method [2]byte
	if _, err := io.ReadFull(client, method[:]); err != nil {
		t.Fatal(err)
	}
	if method != [2]byte{0x05, 0x02} {
		t.Fatalf("unexpected method response: %v", method)
	}
	if _, err := client.Write([]byte{0x01, 0x01, 'u', 0x01, 'p'}); err != nil {
		t.Fatal(err)
	}
	var auth [2]byte
	if _, err := io.ReadFull(client, auth[:]); err != nil {
		t.Fatal(err)
	}
	if auth != [2]byte{0x01, 0x00} {
		t.Fatalf("unexpected auth response: %v", auth)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRequireLoopbackListen(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:1080", "[::1]:1080", "localhost:1080"} {
		if err := requireLoopbackListen(addr); err != nil {
			t.Fatalf("%s should be accepted: %v", addr, err)
		}
	}
	if err := requireLoopbackListen("0.0.0.0:1080"); err == nil {
		t.Fatal("expected non-loopback listener to be rejected")
	}
}
