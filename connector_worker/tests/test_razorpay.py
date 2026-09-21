import json
from pathlib import Path
import unittest

from syne_connectors.manifest import validate_manifest
from syne_connectors.records import normalize_record

FIXTURES=Path(__file__).parent/"fixtures"


class RazorpayExamples(unittest.TestCase):
    def test_documented_settlements_keep_missing_currency_unknown(self):
        m=validate_manifest((FIXTURES/"razorpay.json").read_bytes())
        example=json.loads((FIXTURES/"razorpay-fetch-all-example.json").read_text())
        results=[normalize_record(m,m["streams"][0],row) for row in example["items"]]
        self.assertEqual(len(results),2)
        self.assertTrue(all(result["payload"]["fields"]["currency"] is None for result in results))
        self.assertEqual(results[0]["payload"]["fields"]["amount"],"9973635")

    def test_documented_recon_entity_ids_and_refund_links_are_preserved(self):
        m=validate_manifest((FIXTURES/"razorpay.json").read_bytes())
        example=json.loads((FIXTURES/"razorpay-fetch-recon-example.json").read_text())
        results=[normalize_record(m,m["streams"][1],row) for row in example["items"]]
        self.assertEqual(len({r["id"] for r in results}),4)
        refund=next(r for r in results if r["payload"]["fields"]["type"]=="refund")
        self.assertEqual(json.loads(refund["id"]),["rfnd_DGRcGzZSLyEdg1","refund"])
        self.assertEqual(refund["payload"]["fields"]["debit"],"242500")
        self.assertEqual(refund["payload"]["fields"]["payment_id"],"pay_DEXq1pACSqFxtS")
        self.assertEqual(refund["payload"]["source"],example["items"][1])
