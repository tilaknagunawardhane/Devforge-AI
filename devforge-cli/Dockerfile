# Stage 1: Build the CLI
FROM golang:1.22 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Build a statically linked binary
RUN CGO_ENABLED=0 GOOS=linux go build -o devforge main.go

# Stage 2: Create the final lightweight image
FROM alpine:latest
WORKDIR /workspace
# Copy the binary to a global system path so it can run from anywhere
COPY --from=builder /app/devforge /usr/local/bin/devforge
RUN chmod +x /usr/local/bin/devforge

# Execute by the global command name
ENTRYPOINT ["devforge"]