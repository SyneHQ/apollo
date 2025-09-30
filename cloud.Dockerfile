# Build stage
FROM golang:1.24-alpine AS builder

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
FROM gcr.io/distroless/static-debian12 AS runtime

# Copy the binary from builder stage
COPY --from=builder /app/main /app/main
COPY --from=builder /app/jobs.yml /app/jobs.yml

# Expose port (adjust as needed)
EXPOSE 6901

ENV JOBS_PROVIDER=cloudrun

# Set entrypoint script to allow mounting docker socket
ENTRYPOINT ["/app/main"]