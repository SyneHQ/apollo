# SyneHQ Analytics Container

This directory contains the source code and build files for the `synehq/analytics:latest` container used in the Apollo job system examples.

## Overview

The analytics container is a Bun-based application that demonstrates how to create reusable job containers that can handle different types of analytics tasks with runtime parameter overrides.

## Features

- **Multiple Job Types**: Supports export, report, and aggregation jobs
- **Environment Variable Injection**: Automatically receives Infisical secrets and client overrides
- **Database Integration**: Connects to PostgreSQL databases
- **Redis Caching**: Optional Redis integration for result caching
- **API Notifications**: Sends completion notifications via API
- **Flexible Output**: Supports JSON and other output formats

## Files

- `Dockerfile`: Container definition using Bun base image
- `package.json`: Node.js dependencies and scripts
- `index.ts`: Main analytics application logic
- `rover`: Entry point script for the container
- `build.sh`: Build and deployment script

## Environment Variables

The container expects the following environment variables (injected by Infisical or client overrides):

- `DATABASE_URL`: PostgreSQL connection string
- `API_KEY`: API key for notifications
- `REDIS_URL`: Redis connection string (optional)
- `LOG_LEVEL`: Logging level (default: info)
- `EXECUTION_ID`: Unique execution identifier
- `USER_ID`: User identifier

## Usage

### Building the Container

```bash
# Build the container
./build.sh --build

# Build and push to registry
./build.sh --build --push

# Test the container locally
./build.sh --test
```

### Running Analytics Jobs

```bash
# Export job
docker run --rm \
  -e DATABASE_URL="postgresql://user:pass@host:5432/db" \
  -e API_KEY="your-api-key" \
  -e EXECUTION_ID="exec-123" \
  -e USER_ID="user-456" \
  synehq/analytics:latest analytics \
  --query-type export \
  --database production \
  --output-format json \
  --parallelism 4

# Report job
docker run --rm \
  -e DATABASE_URL="postgresql://user:pass@host:5432/db" \
  -e EXECUTION_ID="exec-124" \
  -e USER_ID="user-789" \
  synehq/analytics:latest analytics \
  --query-type report \
  --database analytics \
  --output-format json \
  --parallelism 2

# Aggregation job
docker run --rm \
  -e DATABASE_URL="postgresql://user:pass@host:5432/db" \
  -e REDIS_URL="redis://host:6379" \
  -e EXECUTION_ID="exec-125" \
  -e USER_ID="user-101" \
  synehq/analytics:latest analytics \
  --query-type aggregation \
  --database warehouse \
  --output-format parquet \
  --parallelism 8
```

### Health Checks

```bash
# Health check
docker run --rm synehq/analytics:latest health

# Version check
docker run --rm synehq/analytics:latest version
```

## Integration with Apollo

This container is designed to work seamlessly with the Apollo job system:

1. **Infisical Secrets**: Database credentials, API keys, and other secrets are automatically injected
2. **Client Overrides**: Runtime parameters like execution ID, user ID, and job-specific settings
3. **Resource Limits**: CPU and memory limits are applied by the runner
4. **Logging**: Structured logging with configurable levels

## Development

### Local Development

```bash
# Install dependencies
bun install

# Run in development mode
bun run dev

# Run specific job type
bun run index.ts analytics --query-type export --database testdb --output-format json
```

### Testing

The container includes comprehensive testing capabilities:

- Health checks
- Version verification
- Sample job execution
- Environment variable validation

## Architecture

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   Apollo Runner │───▶│ Analytics        │───▶│ PostgreSQL      │
│                 │    │ Container        │    │ Database        │
└─────────────────┘    └──────────────────┘    └─────────────────┘
         │                       │                       │
         │                       ▼                       │
         │              ┌──────────────────┐             │
         │              │ Redis Cache      │             │
         │              │ (Optional)       │             │
         │              └──────────────────┘             │
         │                       │                       │
         │                       ▼                       │
         │              ┌──────────────────┐             │
         │              │ API Notifications│             │
         │              │ (Optional)       │             │
         │              └──────────────────┘             │
         │                                               │
         ▼                                               ▼
┌─────────────────┐                            ┌─────────────────┐
│ Infisical       │                            │ Client          │
│ Secrets         │                            │ Overrides       │
└─────────────────┘                            └─────────────────┘
```

This container demonstrates the power of the reusable job pattern where a single container definition can handle multiple scenarios through runtime parameter overrides.
