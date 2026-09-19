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
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /worm

COPY --from=builder /bin/worm /bin/worm
COPY packs/ /worm/packs/
COPY schemas/ /worm/schemas/

EXPOSE 514/udp 514/tcp 8080/tcp 9090/tcp
VOLUME ["/data"]

ENTRYPOINT ["/bin/worm"]
CMD ["-syslog-udp", ":514", "-syslog-tcp", ":514", "-http", ":8080", "-ui", ":9090", "-inbox", "/data/inbox", "-output-file", "/data/output/normalized.ndjson", "-packs-dir", "/worm/packs", "-db", "/data/worm.db"]
