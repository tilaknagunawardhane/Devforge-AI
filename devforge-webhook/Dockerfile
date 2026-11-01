# Stage 1: build a static binary
FROM golang:1.22 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o webhook .

# Stage 2: tiny runtime image, non-root
FROM alpine:3.20
RUN adduser -D -u 10001 app && mkdir /data && chown app /data
COPY --from=builder /app/webhook /usr/local/bin/webhook
USER app
# Incident database lives here; mount a volume to keep it across restarts
VOLUME /data
EXPOSE 9090
ENTRYPOINT ["webhook"]
