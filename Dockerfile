# Build stage
FROM golang:1.24-alpine AS builder

ENV PORT=6910

# Set working directory
WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main cmd/main.go

# Final stage - with Docker CLI
FROM docker:28.4.0-cli-alpine3.22 AS runtime

# Copy the binary from builder stage
COPY --from=builder /app/main /app/main
COPY --from=builder /app/jobs.yml /app/jobs.yml

# Expose port (adjust as needed)
EXPOSE $PORT

ENV JOBS_PROVIDER='local'

# Set entrypoint script to allow mounting docker socket
ENTRYPOINT ["/app/main"]

# The docker socket can be mounted at runtime with:
#   -v /var/run/docker.sock:/var/run/docker.sock
