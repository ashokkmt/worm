# Multi-stage build for static WORM binary
FROM golang:alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY internal/ ./internal/
COPY web/ ./web/
COPY cmd/worm/ ./cmd/worm/

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/worm cmd/worm/main.go

FROM alpine:3.20

RUN apk --no-cache add ca-certificates tzdata
WORKDIR /worm

COPY --from=builder /bin/worm /bin/worm
COPY packs/ /worm/packs/
COPY schemas/ /worm/schemas/

EXPOSE 514/udp 514/tcp 8080/tcp 9090/tcp

VOLUME ["/data"]

ENTRYPOINT ["/bin/worm"]
CMD ["-syslog-udp", ":514", "-syslog-tcp", ":514", "-http", ":8080", "-ui", ":9090", "-inbox", "/data/inbox", "-output-file", "/data/output/normalized.ndjson", "-packs", "/worm/packs", "-db", "/data/worm.db"]
