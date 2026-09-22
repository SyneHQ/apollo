"""Loaded only by the separate acceptance image, never the production image."""
import os

try:
    from shopify_fixture.transport import install
    install()
except BaseException:
    # Python normally ignores sitecustomize failures; that could use real DNS.
    os.write(2, b"Shopify fixture bootstrap failed; worker was not started.\n")
    os._exit(78)
