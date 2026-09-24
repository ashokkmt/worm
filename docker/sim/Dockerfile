# Multi-stage build for lightweight logsim container
FROM golang:alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY internal/ ./internal/
COPY cmd/logsim/ ./cmd/logsim/

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/logsim cmd/logsim/main.go

# Minimal runtime image
FROM alpine:3.20

RUN apk --no-cache add ca-certificates tzdata && \
    addgroup -g 10001 -S logsim && \
    adduser -u 10001 -S logsim -G logsim

COPY --from=builder /bin/logsim /bin/logsim
USER 10001:10001

ENTRYPOINT ["/bin/logsim"]
CMD ["--source", "all", "--scenario", "mixed", "--format", "stdout"]
