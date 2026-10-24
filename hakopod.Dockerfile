# Build stage
FROM golang:1.26.8-alpine AS builder

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

# Final stage - no Docker CLI; jobs run as Hakopod invocations
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

# Copy the binary from builder stage
COPY --from=builder /app/main /app/main
# Mount the reviewed Hakopod jobs file over this at /app/jobs.yml (see PRODUCTION.md)
COPY --from=builder /app/jobs.yml /app/jobs.yml

# Expose port (adjust as needed)
EXPOSE $PORT

ENV JOBS_PROVIDER=hakopod

ENTRYPOINT ["/app/main"]
