# sol

`sol` is a small application-level TCP tunnel. It gives applications a local SOCKS5 proxy and carries SOCKS5 `CONNECT` streams over an authenticated WebSocket to a remote egress server.

It is intentionally not a TUN/TAP or whole-device VPN. SOCKS5 `CONNECT` (TCP) is the supported traffic model.

## Transport

Render Web Services expose HTTP(S) through Render's frontend rather than a raw public TCP port. WebSockets are supported, so the Render-compatible path is:

```text
application -> SOCKS5 127.0.0.1:1080 -> sol client -> WSS/HTTPS -> sol server -> target TCP service
```

Current clients use a persistent multiplexed transport: one authenticated WSS connection carries many independent SOCKS5 TCP streams. This avoids doing a new DNS + TCP + TLS + WebSocket handshake for every browser connection, which matters substantially on high-latency mobile networks.

The server exposes:

- `/mux` - optimized persistent multiplexed transport.
- `/ws` - original one-WebSocket-per-SOCKS-stream transport for v0.1.x compatibility.

You can continue configuring the client with a URL ending in `/ws`. A current client automatically derives the matching `/mux` URL and uses it when the server supports it. If `/mux` is not available yet, it falls back to the legacy `/ws` endpoint.

The multiplexed transport uses keepalives, flow control, a larger stream window for high-RTT links, longer TLS handshake tolerances, Happy Eyeballs fallback, and TLS session resumption for reconnects.

On a host that exposes raw TCP directly, `sol direct` can instead provide an authenticated SOCKS5 listener without the WebSocket hop.

## Build

```sh
go build -o sol ./cmd/sol
```

Prebuilt binaries are also published in GitHub Releases for Linux, Windows, and macOS on amd64 and arm64.

## Render server

The included `render.yaml` creates a free Docker Web Service and asks Render to generate a 256-bit `SOL_TOKEN`.

Run the server manually with:

```sh
SOL_TOKEN='replace-with-a-long-random-secret' PORT=10000 ./sol server
```

Health check:

```sh
curl https://YOUR-SERVICE.onrender.com/healthz
```

Custom domains work as well; point `SOL_SERVER_URL` at the custom HTTPS/WSS hostname after its certificate and DNS are active.

Render free services can spin down when idle. A connected optimized client sends periodic mux keepalives, so an active client session normally keeps the WebSocket alive.

## Local SOCKS5 client

Set the same token that the server uses:

```sh
SOL_SERVER_URL='wss://YOUR-SERVICE.onrender.com/ws' \
SOL_TOKEN='replace-with-the-server-secret' \
./sol client
```

The SOCKS5 listener defaults to `127.0.0.1:1080` and is deliberately restricted to loopback.

Example:

```sh
curl --proxy socks5h://127.0.0.1:1080 https://example.com/
```

When the optimized connection is established, the client logs a line similar to:

```text
optimized multiplexed tunnel connected to wss://YOUR-SERVICE.onrender.com/mux
```

After that, new SOCKS5 TCP connections reuse the same WSS session instead of repeating TLS handshakes.

## v2rayNG / Android wrappers

`sol` currently implements SOCKS5 TCP `CONNECT`, not SOCKS5 UDP ASSOCIATE. Some Android proxy/VPN wrappers probe the SOCKS server with UDP or BIND requests. Current clients reject those probes quietly instead of flooding the log.

For best results when placing v2rayNG in front of `sol`, prefer TCP proxying and disable UDP/QUIC through this outbound when the wrapper allows it. Applications that can fall back from QUIC/HTTP3 to TCP/HTTP2 will then use the supported path.

## Direct SOCKS5 mode

For a VPS/container platform that exposes raw TCP ports:

```sh
SOL_USERNAME='user' \
SOL_PASSWORD='strong-password' \
SOL_LISTEN='0.0.0.0:1080' \
./sol direct
```

Direct mode refuses to start without username/password authentication so it does not accidentally become an open public proxy.

## Container

```sh
docker build -t sol .
docker run --rm -p 10000:10000 \
  -e SOL_TOKEN='replace-with-a-long-random-secret' \
  sol
```

The runtime image is Alpine with CA certificates and an unprivileged user.

## Configuration

| Mode | Variable | Default | Purpose |
|---|---|---|---|
| server | `PORT` | `10000` | HTTP/WebSocket listen port |
| server | `SOL_TOKEN` | required | Bearer token, minimum 16 characters |
| client | `SOL_SERVER_URL` | required | `wss://.../ws` or `wss://.../mux` endpoint; `/ws` is automatically upgraded to `/mux` when available |
| client | `SOL_TOKEN` | required | Same bearer token as the server |
| client | `SOL_LISTEN` | `127.0.0.1:1080` | Local SOCKS5 listener; loopback only |
| direct | `SOL_LISTEN` | `0.0.0.0:1080` | Raw SOCKS5 listener |
| direct | `SOL_USERNAME` | required | SOCKS5 username |
| direct | `SOL_PASSWORD` | required | SOCKS5 password |

## Security and limitations

- Use `wss://` for remote connections.
- WebSocket endpoints require a bearer token before upgrade.
- The local client listener cannot bind to a non-loopback address.
- Direct mode requires SOCKS5 username/password authentication.
- TCP `CONNECT` only. UDP ASSOCIATE and BIND are not implemented.
- Multiplexing removes repeated connection handshakes but cannot remove physical Internet RTT between the device, CDN/custom domain, Render region, and destination.
- This is an application proxy, not packet-level routing; applications must support SOCKS5 or be configured through a proxy-aware wrapper.
- Render Free is suitable for hobby/testing use and has resource and usage limits.

## CI

GitHub Actions performs formatting, vet, unit/integration tests, binary build, Docker build, container startup, and `/healthz` verification. Integration tests cover both the legacy tunnel and the persistent multiplexed tunnel, including verification that multiple logical connections reuse one WebSocket session.
