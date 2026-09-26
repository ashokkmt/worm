FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/logsim/ ./cmd/logsim/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/logsim ./cmd/logsim
FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /bin/logsim /bin/logsim
USER 10001:10001
ENTRYPOINT ["/bin/logsim"]
CMD ["--source", "all", "--scenario", "mixed", "--format", "stdout"]
