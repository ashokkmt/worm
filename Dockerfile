# Stage 1: Build the embedded React frontend
FROM node:20-alpine AS frontend
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Stage 2: Build the static Go WORM binary
FROM golang:alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY internal/ ./internal/
COPY cmd/worm/ ./cmd/worm/
COPY --from=frontend /web/dist ./web/dist
COPY web/embed.go ./web/embed.go

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/worm ./cmd/worm

# Stage 3: Minimal production runtime image
FROM alpine:3.20
RUN apk --no-cache add ca-certificates tzdata libcap && \
    addgroup -g 10001 -S worm && \
    adduser -u 10001 -S worm -G worm && \
    mkdir -p /data/inbox /data/output /worm/packs /worm/sources /worm/sinks && \
    chown -R worm:worm /data /worm

WORKDIR /worm

COPY --from=builder /bin/worm /bin/worm
RUN setcap 'cap_net_bind_service=+ep' /bin/worm

COPY --chown=worm:worm packs/ /worm/packs/
COPY --chown=worm:worm schemas/ /worm/schemas/
COPY --chown=worm:worm sources/ /worm/sources/
COPY --chown=worm:worm sinks/ /worm/sinks/

USER 10001:10001

EXPOSE 514/udp 514/tcp 601/tcp 1514/udp 1514/tcp 1601/tcp 6514/tcp 7514/tcp 8080/tcp 9090/tcp
VOLUME ["/data"]

ENTRYPOINT ["/bin/worm"]
CMD ["-syslog-udp", ":514", "-syslog-tcp", ":514", "-syslog-tls", ":6514", "-http", ":8080", "-ui", ":9090", "-inbox", "/data/inbox", "-output-file", "/data/output/normalized.ndjson", "-packs-dir", "/worm/packs", "-sources-dir", "/worm/sources", "-sinks-dir", "/worm/sinks", "-db", "/data/worm.db"]
