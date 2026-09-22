import io
import json
import unittest
from unittest.mock import patch
from uuid import uuid4

from syne_connectors.diagnostics import public_failure
from syne_connectors.manifest import ConnectorError
from syne_connectors.worker import main


class DiagnosticTests(unittest.TestCase):
    def test_unknown_source_or_exception_text_never_enters_public_output(self):
        for error in [ValueError("secret-canary"), ConnectorError("source_access_denied: secret-canary"), ConnectorError("unrecognized")]:
            self.assertEqual(public_failure(error), "connector_worker_failed")
        self.assertEqual(public_failure(ConnectorError("shopify_history_scope_required")), "source_access_required")
        self.assertEqual(public_failure(ConnectorError("shopify_parent_changed")), "source_changed")
        self.assertEqual(public_failure(ConnectorError("shopify_dense_window")), "source_limit")

    def test_failure_report_is_bound_to_the_run_and_retains_nonzero_exit(self):
        run_id = str(uuid4()); output = io.StringIO()
        with patch.dict("os.environ", {"CONNECTOR_RUN_ID": run_id}), patch("syne_connectors.worker.signal.signal"), \
             patch("syne_connectors.worker.run_worker", side_effect=ConnectorError("source_money_format_changed")), patch("sys.stderr", output):
            self.assertEqual(main(), 1)
        self.assertEqual(json.loads(output.getvalue()), {"status": "failed", "run_id": run_id, "error": "source_schema_changed"})
