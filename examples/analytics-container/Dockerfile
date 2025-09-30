# Use Bun as the base image
FROM oven/bun:alpine

# Install Redis and SQLite
RUN apk add --no-cache redis sqlite

# Set working directory
WORKDIR /app

# Copy package files
COPY package.json bun.lock ./

# Install dependencies
RUN bun install

# Copy application files
COPY . .

# Make rover script executable
RUN chmod +x /app/rover

# Create startup script with SQLite initialization
RUN echo '#!/bin/sh' > /app/start.sh && \
    echo 'redis-server --daemonize yes' >> /app/start.sh && \
    echo 'sqlite3 /app/analytics.db < /app/seed.sql' >> /app/start.sh && \
    echo 'exec bun run index.ts analytics --query-type export --database analytics --output-format json' >> /app/start.sh && \
    chmod +x /app/start.sh

# Create entrypoint script to handle different commands
RUN echo '#!/bin/sh' > /app/entrypoint.sh && \
    echo 'case "$1" in' >> /app/entrypoint.sh && \
    echo '  health)' >> /app/entrypoint.sh && \
    echo '    echo "Health check: OK"' >> /app/entrypoint.sh && \
    echo '    exit 0' >> /app/entrypoint.sh && \
    echo '    ;;' >> /app/entrypoint.sh && \
    echo '  version)' >> /app/entrypoint.sh && \
    echo '    if [ -f /app/rover ]; then' >> /app/entrypoint.sh && \
    echo '      /app/rover version' >> /app/entrypoint.sh && \
    echo '    else' >> /app/entrypoint.sh && \
    echo '      echo "rover not found"' >> /app/entrypoint.sh && \
    echo '      exit 1' >> /app/entrypoint.sh && \
    echo '    fi' >> /app/entrypoint.sh && \
    echo '    ;;' >> /app/entrypoint.sh && \
    echo '  *)' >> /app/entrypoint.sh && \
    echo '    exec /app/start.sh' >> /app/entrypoint.sh && \
    echo '    ;;' >> /app/entrypoint.sh && \
    echo 'esac' >> /app/entrypoint.sh && \
    chmod +x /app/entrypoint.sh

# Set the entrypoint
ENTRYPOINT ["/app/entrypoint.sh"]

# Set the default command
CMD ["analytics"]
