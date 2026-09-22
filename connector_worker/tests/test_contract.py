import copy
import json
from pathlib import Path
import unittest

from syne_connectors.manifest import ConnectorError, canonical, configuration, content_digest, validate_manifest

FIXTURES = Path(__file__).parent / "fixtures"


class ContractTests(unittest.TestCase):
    def test_builtin_contracts_and_digests(self):
        expected = json.loads((FIXTURES / "digests.json").read_text())
        for name, digest in expected.items():
            with self.subTest(name=name):
                m = validate_manifest((FIXTURES / name).read_bytes(), digest)
                self.assertEqual(content_digest(m), digest)

    def test_unsafe_semantics(self):
        base = json.loads((FIXTURES / "razorpay.json").read_text())
        changes = [
            lambda m: m["configuration"][1].update(default="secret"),
            lambda m: m["runtime"]["origin"].update(host="secret.internal"),
            lambda m: m["streams"][0]["request"].update(path="/v1/../secrets"),
            lambda m: m["streams"][0].update(primaryKey=["missing"]),
            lambda m: m["streams"][0]["request"]["parameters"].append({"name":"token","value":{"from":"config","field":"key_secret"}}),
            lambda m: m["publisher"].update(id="impostor"),
            lambda m: m["capabilities"].update(incremental=True),
            lambda m: m["runtime"]["auth"].update(passwordField="key_id"),
            lambda m: m["configuration"].append(m["configuration"][0]),
            lambda m: m.update(documentation="http://example.com"),
        ]
        for change in changes:
            m = copy.deepcopy(base); change(m)
            with self.assertRaises(ConnectorError):
                validate_manifest(canonical(m))

    def test_configuration_and_parser_errors_do_not_echo_secrets(self):
        m = validate_manifest((FIXTURES / "razorpay.json").read_bytes())
        values = {"key_id":"id", "key_secret":"private-value", "year":2026, "month":9}
        self.assertEqual(configuration(m, values), values)
        for bad in [{**values,"month":True}, {**values,"unknown":"private-value"}, {**values,"month":13}]:
            with self.assertRaises(ConnectorError) as error:
                configuration(m, bad)
            self.assertNotIn("private-value", str(error.exception))
        with self.assertRaises(ConnectorError):
            validate_manifest(b'{"private-value":1,"private-value":2}')

    def test_third_manifest_needs_no_shared_form_or_transport_change(self):
        m = json.loads((FIXTURES / "razorpay.json").read_text())
        m.update(id="fixture/ledger", publisher={"id":"fixture","name":"Fixture","url":"https://example.com"})
        m["runtime"]["origin"]["host"] = "api.example.com"
        self.assertEqual(validate_manifest(canonical(m))["id"], "fixture/ledger")
