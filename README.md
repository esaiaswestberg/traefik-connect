# traefik-connect

`traefik-connect` is a distributed control plane for Traefik in a small homelab or self-hosted environment.

It replaces Docker Swarm, Consul, or remote Docker access with two lightweight services:

- a worker-side discovery agent that watches the local Docker daemon
- a master-side receiver that accepts normalized snapshots and creates master-local stub containers with Traefik labels

## Architecture

- The worker agent scans Docker locally via the Unix socket.
- Only containers that opt in are exported. The preferred opt-in label is `traefik-sync.enable=true`.
- The agent parses Traefik HTTP labels, resolves a backend URL the worker host can actually reach, and sends a full snapshot to the master over HTTPS or HTTP with bearer auth plus HMAC signing.
- The master receiver validates the snapshot, stores it on disk, and reconciles master-local stub containers that Traefik discovers through the Docker provider.
- The stub containers forward HTTP traffic to the worker agent proxy endpoint, which then forwards to the real application container on the worker.
- The master example also defines an ACME resolver named `letsencrypt` so routers that set `tls.certresolver=letsencrypt` are accepted.
- The worker agent example runs with host networking so it can reach container IPs without publishing application ports.

### Backend resolution order

1. `traefik-sync.backend.url`
2. `traefik-sync.backend.host` + `traefik-sync.backend.port`
3. Container IP on the worker network or host-network address

If resolution fails, the worker skips that container and logs the reason.

## Repository Layout

- `cmd/traefik-connect`: CLI entrypoint
- `internal/worker`: worker runtime
- `internal/receiver`: master runtime, persistence, and validation
- `internal/parse`: label parsing and backend resolution
- `internal/stub`: master-side request forwarding stub container
- `examples/`: static Traefik config and example labeled workload

## Build

```bash
docker build -t traefik-connect .
go test ./...
```

## Run the receiver on the master

```bash
export RECEIVER_TOKEN=change-me
docker compose -f docker-compose.master.yml up --build
```

This starts:

- the receiver on port `8080`
- Traefik on ports `80` and `443`
- an ACME storage directory at `./acme`
- a shared Docker network named `traefik-connect`
- Traefik's Docker provider watching the local Docker socket

The receiver exposes:

- `GET /healthz`
- `GET /readyz`
- `GET /v1/status`
- `POST /v1/snapshot`

## Run the worker on a LAN host

```bash
export AGENT_WORKER_ID=worker-a
export AGENT_ADVERTISE_ADDR=192.168.1.20
export AGENT_MASTER_URL=http://192.168.1.10:8080
export AGENT_TOKEN=change-me
export AGENT_PROXY_LISTEN_ADDR=:8090
docker compose -f docker-compose.worker.yml up --build
```

The worker agent exposes:

- `GET /healthz`
- `GET /readyz`
- `GET /debug/state`
- `POST /v1/proxy`

## Labeling example

```yaml
labels:
  traefik-sync.enable: "true"
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

## Security notes

- Use a long random shared token.
- Prefer HTTPS between worker and receiver in anything beyond a lab.
- The receiver rejects requests with bad auth, invalid signatures, oversized bodies, and stale timestamps.
- The bundled master example uses the Let's Encrypt staging CA so the stack can be tested without production issuance pressure.
- If you use the `letsencrypt` resolver, make sure your domain points at the master and port 80 is reachable for ACME HTTP-01 validation.
- The worker agent proxy endpoint should be reachable only on the worker LAN or a private network.

## Operational behavior

- The worker performs a startup scan, reacts to Docker events, and performs periodic full resyncs.
- The receiver stores state on disk and prunes workers that have not refreshed within the configured TTL.
- Stub containers are reconciled deterministically from worker snapshots.

## Limitations in v1

- HTTP only
- no Swarm, Kubernetes, Consul, Nomad, TCP, or UDP support
- no UI
