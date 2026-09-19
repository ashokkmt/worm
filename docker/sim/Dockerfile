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

RUN apk --no-cache add ca-certificates tzdata
COPY --from=builder /bin/logsim /bin/logsim

ENTRYPOINT ["/bin/logsim"]
CMD ["--source", "all", "--scenario", "mixed", "--format", "stdout"]
