FROM golang:1.22-alpine AS build

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/traefik-connect ./cmd/traefik-connect

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/traefik-connect /usr/local/bin/traefik-connect
ENTRYPOINT ["/usr/local/bin/traefik-connect"]
