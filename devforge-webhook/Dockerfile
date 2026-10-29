# Stage 1: build a static binary
FROM golang:1.22 AS builder
WORKDIR /app
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o webhook main.go

# Stage 2: tiny runtime image, non-root
FROM alpine:3.20
RUN adduser -D -u 10001 app
COPY --from=builder /app/webhook /usr/local/bin/webhook
USER app
EXPOSE 9090
ENTRYPOINT ["webhook"]
