FROM python:3.12-slim@sha256:2f17fc044b579bab302c2e8054d3a686e2cb9a83de48e70534b94cd8ebbe06a9

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    RUNTIME__DLT_TELEMETRY=false \
    DLT_DATA_DIR=/tmp/dlt

WORKDIR /opt/connector
COPY requirements.lock pyproject.toml ./
COPY syne_connectors ./syne_connectors
RUN pip install --no-cache-dir -r requirements.lock \
    && pip install --no-cache-dir --no-deps --no-build-isolation . \
    && groupadd --gid 10001 connector \
    && useradd --uid 10001 --gid 10001 --home-dir /tmp --no-create-home --shell /usr/sbin/nologin connector

USER 10001:10001
WORKDIR /tmp
CMD ["syne-connector-worker"]
