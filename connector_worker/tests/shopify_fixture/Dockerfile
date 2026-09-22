# Supply an already installed immutable worker image. This file adds no packages.
ARG BASE_WORKER_IMAGE
FROM ${BASE_WORKER_IMAGE}
LABEL syne.fixture="shopify-refund-acceptance" \
      syne.fixture.boundary="provider DNS/TLS substituted; not a production worker"
COPY tests/shopify_fixture /opt/syne-shopify-fixture/shopify_fixture
COPY tests/shopify_fixture/sitecustomize.py /opt/syne-shopify-fixture/sitecustomize.py
ENV PYTHONPATH=/opt/syne-shopify-fixture
