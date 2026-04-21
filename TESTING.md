# Testing Guide

This repo ships a small worker-side testing profile that makes it easy to exercise the proxy path with:

- long-running HTTP connections
- server-sent events
- file uploads
- range-based downloads
- WebSockets

The testing services are **opt-in**. They only start when you enable the `testing` compose profile.

## Start the stacks

Master:

```bash
RECEIVER_TOKEN='change-me' docker compose -f docker-compose.master.yml up --build
```

Worker with test services:

```bash
AGENT_WORKER_ID=worker-a \
AGENT_ADVERTISE_ADDR=<worker-lan-ip> \
AGENT_MASTER_URL=http://<master-lan-ip>:18180 \
AGENT_TOKEN=change-me \
docker compose --profile testing -f docker-compose.worker.yml up --build
```

The testing profile starts two extra containers:

- `streamlab` at `https://stream.example.test`
- `medialab` at `https://media.example.test`

If you are testing on one machine, add `--resolve ...:127.0.0.1` to the curl examples below.

## Long-running HTTP

Use the `/wait` endpoint to keep a connection open without streaming data.

```bash
curl -k -v \
  https://stream.example.test/wait?duration=45s \
  --resolve stream.example.test:443:127.0.0.1
```

Expected result:

- the connection stays open until the timeout expires
- the response body is `waited 45s`

## Streaming events

Use the SSE endpoint to verify the proxy can hold a live stream open.

```bash
curl -k -N \
  https://stream.example.test/events \
  --resolve stream.example.test:443:127.0.0.1
```

Expected result:

- the connection stays open for several seconds
- you see lines like `data: streamlab 0`
- the output arrives incrementally instead of all at once

## File uploads

Use `--data-binary` for raw uploads or `-F` for multipart uploads.

Raw upload:

```bash
dd if=/dev/urandom of=/tmp/traefik-connect-upload.bin bs=1m count=20
curl -k -X POST \
  --data-binary @/tmp/traefik-connect-upload.bin \
  https://stream.example.test/upload \
  --resolve stream.example.test:443:127.0.0.1
```

Multipart upload:

```bash
curl -k -F file=@/tmp/traefik-connect-upload.bin \
  https://stream.example.test/upload \
  --resolve stream.example.test:443:127.0.0.1
```

Expected result:

- the request completes without buffering errors
- the response says `uploaded=<bytes>`

## WebSockets

Use `wscat` to verify upgrade handling.

```bash
wscat -c wss://127.0.0.1/ws \
  -H "Host: stream.example.test" \
  --no-check
```

Expected result:

- the connection upgrades successfully
- messages you send are echoed back

## Range downloads

Use the media service to test video-style range requests and partial downloads.

Download the full file:

```bash
curl -k -o /tmp/traefik-connect-sample.bin \
  https://media.example.test/file \
  --resolve media.example.test:443:127.0.0.1
```

Fetch a range:

```bash
curl -k -D - -o /tmp/traefik-connect-range.bin \
  -H 'Range: bytes=0-1048575' \
  https://media.example.test/file \
  --resolve media.example.test:443:127.0.0.1
```

Expected result:

- the range request returns `206 Partial Content`
- the file downloads without timing out

## What to watch for

- `404 page not found` usually means the host name or router labels are wrong.
- `unexpected eof while reading` usually means Traefik is published on the wrong host/container ports.
- `unauthorized` usually means the shared token does not match between master and worker.
- If a large upload stalls, check the worker proxy listener and the master stub for timeout settings.
