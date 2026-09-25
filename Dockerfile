FROM golang:1.25-alpine AS builder
WORKDIR /src
ENV GOPROXY=off GOSUMDB=off
COPY go.mod go.sum ./
COPY vendor/ ./vendor/
COPY internal/ ./internal/
COPY cmd/worm/ ./cmd/worm/
COPY web/embed.go ./web/embed.go
COPY web/dist/ ./web/dist/
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -ldflags="-s -w" -o /bin/worm ./cmd/worm \
    && mkdir -p /image-data/inbox /image-data/output

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /bin/worm /bin/worm
COPY --chown=10001:10001 packs/ /worm/packs/
COPY schemas/ /worm/schemas/
COPY --chown=10001:10001 sources/ /worm/sources/
COPY --chown=10001:10001 sinks/ /worm/sinks/
COPY --from=builder --chown=10001:10001 /image-data/ /data/
USER 10001:10001
WORKDIR /worm
EXPOSE 1514/udp 1514/tcp 7514/tcp 8080/tcp 9090/tcp
VOLUME ["/data"]
ENTRYPOINT ["/bin/worm"]
CMD ["-syslog-udp", ":1514", "-syslog-tcp", ":1514", "-syslog-tls", "none", "-http", ":8080", "-ui", ":9090", "-inbox", "/data/inbox", "-output-file", "/data/output/normalized.ndjson", "-packs-dir", "/worm/packs", "-sources-dir", "/worm/sources", "-sinks-dir", "/worm/sinks", "-db", "/data/worm.db"]
