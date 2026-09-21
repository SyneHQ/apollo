import io
from hashlib import sha256
from pathlib import Path
import unittest

from syne_connectors.csv_source import csv_pages
from syne_connectors.handoff import write_pages
from syne_connectors.manifest import ConnectorError, validate_manifest
from syne_connectors.records import normalize_record
from test_pages import Sink, budget


class CSVTests(unittest.TestCase):
    def setUp(self):
        self.manifest=validate_manifest((Path(__file__).parent/"fixtures/merchant-report.json").read_bytes())
        self.values={"file_id":"file-a","source_name":"Merchant export","timezone":"Asia/Kolkata","id_column":"record_id","amount_column":"net","currency_column":"ccy"}

    def pages(self, raw, checkpoint=None):
        return csv_pages(self.manifest,self.values,io.BytesIO(raw),sha256(raw).hexdigest(),checkpoint or {},budget())

    def test_csv_import_preserves_decimal_strings_original_columns_and_formulas(self):
        raw=b'record_id,net,ccy,note\r\none,123456789012345678.10,INR,"=SUM(A1:A2)"\r\ntwo,-19.90,INR,"quoted, text"\r\n'
        sink=Sink();result=write_pages(self.pages(raw),sink,sink.state,"run-csv",lambda row:normalize_record(self.manifest,self.manifest["streams"][0],row),budget())
        self.assertEqual(result["records_committed"],2)
        self.assertEqual(sink.rows["one"]["payload"]["fields"]["amount"],"123456789012345678.10")
        self.assertEqual(sink.rows["one"]["payload"]["source"]["payload"]["note"],"=SUM(A1:A2)")

    def test_resume_uses_verified_file_and_row_position(self):
        self.manifest["limits"]["batchRows"]=1
        raw=b'record_id,net,ccy\none,1.00,USD\ntwo,2.00,USD\nthree,3.00,USD\n'
        pages=list(self.pages(raw));self.assertEqual(len(pages),3)
        resumed=list(self.pages(raw,pages[0].after));self.assertEqual([p.records[0]["id"] for p in resumed],["two","three"])
        with self.assertRaisesRegex(ConnectorError,"file_changed"):
            list(csv_pages(self.manifest,self.values,io.BytesIO(raw+b'four,4.00,USD\n'),sha256(raw).hexdigest(),{},budget()))

    def test_ambiguous_headers_shapes_currency_and_encoding_fail(self):
        cases=[b'record_id,net,ccy,net\na,1,USD,2\n',b'record_id,net\na,1\n',b'record_id,net,ccy\na,1\n',b'record_id,net,ccy\na,1,USD,extra\n',b'record_id,net,ccy\na,1,$\n',b'record_id,net,ccy\n\xff,1,USD\n']
        for raw in cases:
            with self.subTest(raw=raw),self.assertRaises(ConnectorError):list(self.pages(raw))

    def test_empty_report_has_a_durable_terminal_page(self):
        pages=list(self.pages(b'record_id,net,ccy\n'))
        self.assertEqual(len(pages),1);self.assertEqual(pages[0].records,[]);self.assertTrue(pages[0].after["done"])
