# traefik-connect

`traefik-connect` is a distributed Traefik control plane for a small homelab or self-hosted setup with:

- one public master that runs Traefik
- one or more private workers that run application containers
- no Swarm, Kubernetes, Consul, Nomad, or remote Docker access

The worker agent discovers opted-in Docker containers locally. The master receiver turns those snapshots into master-local stub containers. Traefik on the master discovers those stubs through its Docker provider and sends traffic through them back to the worker proxy. The proxy path streams request and response bodies directly, so large uploads, downloads, WebSockets, and long-lived HTTP connections are not wrapped in JSON.

## How it works

1. A worker container opts in with `traefik-connect.enable=true`.
   `traefik.enable=true` remains the final fallback.
2. The worker agent reads the local Docker socket, parses Traefik labels, and resolves a backend URL the worker can actually reach.
3. The agent sends a signed snapshot to the master receiver.
4. The receiver validates the snapshot and creates or updates a local stub container on the master.
5. Traefik sees the stub container through the Docker provider and routes requests to it.
6. The stub reverse-proxies the request to the worker proxy endpoint, which reverse-proxies it to the real application container.

## Backend resolution order

The worker agent resolves the application backend in this order:

1. `traefik-connect.backend.url`
2. `traefik-connect.backend.host` + `traefik-connect.backend.port`
3. A worker-reachable container address derived from Docker metadata

If resolution fails, the container is skipped and the reason is logged.

## Repository layout

- `cmd/traefik-connect`: single binary with `agent`, `receiver`, and `stub` modes
- `internal/worker`: worker sync loop, streaming reverse proxy, and status endpoint
- `internal/receiver`: master API, persistence, and stub reconciliation
- `internal/parse`: Traefik label parsing and backend resolution
- `internal/stub`: master-side HTTP stub that forwards to the worker proxy
- `examples/`: Traefik static config and a labeled worker workload

## Build and test

```bash
go test ./...
docker build -t traefik-connect .
```

For stream/video/upload/WebSocket validation, see [TESTING.md](TESTING.md).

## Master setup

The master stack needs three things:

- the receiver API on host port `18180`
- Traefik on ports `80` and `443`
- a shared Docker socket so Traefik can discover master-local stubs

Then start the master:

```bash
RECEIVER_TOKEN='change-me' docker compose -f docker-compose.master.yml up --build
```

What starts on the master:

- `receiver` on container port `18180`, published on host port `18180`
- `traefik` on container ports `80` and `443`
- ACME storage at `./acme`
- master-local stub containers created by the receiver

### Master API

The receiver exposes:

- `GET /healthz`
- `GET /readyz`
- `GET /v1/status`
- `POST /v1/snapshot`

## Worker setup

The worker example runs the agent with host networking so it can reach internal container addresses without publishing the application port.

```bash
AGENT_WORKER_ID=worker-a \
AGENT_ADVERTISE_ADDR=192.168.1.20 \
AGENT_MASTER_URL=http://192.168.1.10:18180 \
AGENT_TOKEN=change-me \
docker compose -f docker-compose.worker.yml up --build
```

What starts on the worker:

- `agent` on host network port `8081` for status
- `agent` on host network port `8090` for the local streaming proxy
- the master stub listens on `18181` inside the container
- a `whoami` example container with Traefik labels

### Worker API

The agent exposes:

- `GET /healthz`
- `GET /readyz`
- `GET /debug/state`

## Example labels

This is the label set used by the worker example:

```yaml
labels:
  traefik-connect.enable: "true"
  traefik.http.routers.whoami-http.rule: Host(`whoami.example.test`)
  traefik.http.routers.whoami-http.entrypoints: web
  traefik.http.routers.whoami-http.middlewares: secure
  traefik.http.routers.whoami-http.service: whoami-svc
  traefik.http.routers.whoami-https.rule: Host(`whoami.example.test`)
  traefik.http.routers.whoami-https.entrypoints: websecure
  traefik.http.routers.whoami-https.tls: "true"
  traefik.http.routers.whoami-https.tls.certresolver: letsencrypt
  traefik.http.routers.whoami-https.service: whoami-svc
  traefik.http.services.whoami-svc.loadbalancer.server.port: "80"
  traefik.http.services.whoami-svc.loadbalancer.passhostheader: "true"
  traefik.http.middlewares.secure.redirectscheme.scheme: https
  traefik.http.middlewares.secure.redirectscheme.permanent: "true"
```

## Example request flow

For `https://whoami.example.test`:

1. Traefik matches the `whoami-https` router.
2. Traefik connects to the master-local stub container.
3. The stub reverse-proxies the request to `http://<worker-lan-ip>:8090`.
4. The worker proxy reverse-proxies the request to the real application container.
5. The response streams back through the same path in reverse.

## Security notes

- Use a long random shared token for both the agent and receiver.
- Prefer HTTPS between worker and receiver for anything beyond a lab.
- The receiver rejects bad auth, invalid signatures, oversized bodies, and stale timestamps.
- The worker proxy endpoint should be reachable only on the worker LAN or a private network.
- The stub-to-worker hop uses a dedicated internal auth header, while the client request body and response body stream directly through both proxies.

## ACME notes

The bundled master example uses the Let's Encrypt staging CA. That means:

- the sample email is intentionally fake
- ACME issuance is expected to fail until you replace the sample email with a real address
- the example still loads and demonstrates the routing path even if ACME cannot issue a certificate

If you want real certificates:

1. Replace `test@example.com` in `examples/traefik-static.yml` with a real email address.
2. Make sure your domain resolves to the master.
3. Make sure port `80` is reachable from the internet for HTTP-01 validation.

## Operational behavior

- The worker performs a startup scan, watches Docker events, and resyncs periodically.
- The receiver stores worker state on disk and prunes stale workers after the configured TTL.
- Master-local stub containers are reconciled from worker snapshots and recreated when the stub image changes.

## Troubleshooting

- `404 page not found` usually means Traefik did not match a router on `web` or `websecure`.
- `unauthorized` from the browser usually means the master stub and worker proxy are not using the same shared token or the wrong code is running.
- If large uploads or downloads stall, check the worker proxy listener and the master stub listener for timeout settings.
- `client version 1.24 is too old` means Traefik is too old for the Docker daemon on the master and needs a newer image.
- If curl returns `unexpected eof while reading`, check the host-to-container port mapping for Traefik.

## Limitations

- HTTP streaming only.
- No Swarm, Kubernetes, Consul, Nomad, TCP, or UDP support.
- No UI.
