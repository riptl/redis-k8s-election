FROM golang:1.15-alpine AS builder
WORKDIR /app
ADD go.mod main.go ./
RUN go build -o /root/redis-k8s-election

FROM alpine
COPY --from=builder /root/redis-k8s-election /usr/bin/
ENTRYPOINT ["/usr/bin/redis-k8s-election"]
