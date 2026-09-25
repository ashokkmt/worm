FROM golang:1.25-alpine AS builder
WORKDIR /src
ENV GOPROXY=off GOSUMDB=off
COPY go.mod go.sum ./
COPY vendor/ ./vendor/
COPY internal/ ./internal/
COPY cmd/logsim/ ./cmd/logsim/
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -ldflags="-s -w" -o /bin/logsim ./cmd/logsim
FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /bin/logsim /bin/logsim
USER 10001:10001
ENTRYPOINT ["/bin/logsim"]
CMD ["--source", "all", "--scenario", "mixed", "--format", "stdout"]
