# sol

`sol` is a small application-level TCP tunnel. It gives applications a local SOCKS5 proxy and carries each SOCKS5 `CONNECT` stream over an authenticated WebSocket to a remote egress server.

It is intentionally not a TUN/TAP or whole-device VPN. SOCKS5 `CONNECT` (TCP) is the supported traffic model in this first version.

## Why WebSocket on Render?

Render Web Services expose HTTP(S) through Render's frontend rather than a raw public TCP port. WebSockets are supported, so the Render-compatible path is:

```text
application -> SOCKS5 127.0.0.1:1080 -> sol client -> WSS/HTTPS -> sol server -> target TCP service
```

On a host that exposes raw TCP directly, `sol direct` can instead provide an authenticated SOCKS5 listener without the WebSocket hop.

## Build

```sh
go build -o sol ./cmd/sol
```

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

Render free services can spin down when idle, so the first new HTTP/WebSocket connection after inactivity can be delayed while the service starts.

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
| client | `SOL_SERVER_URL` | required | `wss://.../ws` endpoint |
| client | `SOL_TOKEN` | required | Same bearer token as the server |
| client | `SOL_LISTEN` | `127.0.0.1:1080` | Local SOCKS5 listener; loopback only |
| direct | `SOL_LISTEN` | `0.0.0.0:1080` | Raw SOCKS5 listener |
| direct | `SOL_USERNAME` | required | SOCKS5 username |
| direct | `SOL_PASSWORD` | required | SOCKS5 password |

## Security and limitations

- Use `wss://` for remote connections. Render supplies managed TLS on its public endpoint.
- The WebSocket endpoint requires a bearer token before upgrade.
- The local client listener cannot bind to a non-loopback address.
- Direct mode requires SOCKS5 username/password authentication.
- TCP `CONNECT` only. UDP ASSOCIATE and BIND are not implemented.
- This is an application proxy, not packet-level routing; applications must support SOCKS5 or be configured through a proxy-aware wrapper.
- Render Free is suitable for hobby/testing use and has idle spin-down and bandwidth/service-initiated-traffic limits.

## CI

GitHub Actions performs formatting, vet, unit/integration tests, binary build, Docker build, container startup, and `/healthz` verification. The integration test uses an in-process TCP echo service, so it does not require arbitrary external network access after dependencies are downloaded.
