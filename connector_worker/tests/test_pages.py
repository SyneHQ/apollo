from hashlib import sha256
import json
from pathlib import Path
import threading
import time
import unittest
from unittest.mock import patch
from urllib.parse import parse_qs, urlsplit

from syne_connectors.handoff import write_pages, wire_batch
from syne_connectors.manifest import ConnectorError, validate_manifest
from syne_connectors.pages import Page, rest_pages
from syne_connectors.records import normalize_record
from syne_connectors.transport import BoundedSession, Budget, SourceResponse


def budget(): return Budget(time.monotonic()+60, threading.Event())
def response(data):
    result=SourceResponse();result.status_code=200;result._content=json.dumps(data).encode();return result


def normal(row): return {"id":row["id"],"payload":row,"deleted":False}


class Sink:
    def __init__(self): self.calls=[];self.receipts={};self.state={"sequence":0,"checkpoint":{}};self.rows={}
    def commit(self, raw):
        self.calls.append(raw);b=json.loads(raw)
        if b["id"] in self.receipts: return self.receipts[b["id"]]
        if self.state["sequence"]!=b["expected_sequence"]: raise ConnectorError("sequence_conflict")
        self.state={"sequence":b["expected_sequence"]+1,"checkpoint":b["checkpoint"]}
        for r in b["records"]: self.rows[r["id"]]=r
        result={"batch_id":b["id"],"sequence":self.state["sequence"],"records":len(b["records"]),"digest":sha256(raw).hexdigest()}
        self.receipts[b["id"]]=result;return result


class PageTests(unittest.TestCase):
    def fixture(self):
        m=validate_manifest((Path(__file__).parent/"fixtures/razorpay.json").read_bytes())
        m["streams"][0]["request"]["pagination"]["pageSize"]=2
        return m,m["streams"][0],{"key_id":"id","key_secret":"secret","year":2026,"month":9}

    def test_real_dlt_offset_pagination_and_resume(self):
        m,stream,values=self.fixture();seen=[]
        def once(_session,request,url):
            offset=int(parse_qs(url.query)["skip"][0]);seen.append(offset)
            return response({"items":[{"id":str(offset)}]} if offset<4 else {"items":[]})
        with patch.object(BoundedSession,"_once",once):
            pages=list(rest_pages(m,stream,values,{"position":2,"done":False},budget()))
        self.assertEqual(seen,[2,4]);self.assertEqual(pages[-1].after,{"position":4,"done":True})

    def test_cursor_loop_and_missing_cursor_do_not_finish_successfully(self):
        m,stream,values=self.fixture();stream["request"]["pagination"]={"type":"cursor","cursorParam":"after","limitParam":"count","pageSize":2,"nextCursorPath":["next"]}
        for data in [{"items":[{"id":"1"}],"next":"same"},{"items":[]}]:
            with patch.object(BoundedSession,"_once",return_value=response(data)):
                with self.assertRaises(ConnectorError): list(rest_pages(m,stream,values,{},budget()))

    def test_lost_ack_retries_identical_bytes(self):
        sink=Sink();commit=sink.commit;first=True
        def ambiguous(raw):
            nonlocal first
            result=commit(raw)
            if first: first=False;raise ConnectionError()
            return result
        sink.commit=ambiguous
        page=Page([{"id":"one"}],{"position":0,"done":False},{"position":1,"done":True})
        with patch.object(Budget,"wait"):
            result=write_pages([page],sink,sink.state,"run-a",normal,budget())
        self.assertEqual(sink.calls[0],sink.calls[1]);self.assertEqual(len(sink.rows),1);self.assertEqual(result["sequence"],1)

    def test_partial_page_crash_resume_and_changed_page(self):
        sink=Sink();page=Page([{"id":"one"},{"id":"two"},{"id":"three"}],{"position":0,"done":False},{"position":3,"done":True})
        commit=sink.commit
        def fail_second(raw):
            if sink.state["sequence"]==1: raise ConnectorError("worker_crashed")
            return commit(raw)
        sink.commit=fail_second
        with self.assertRaises(ConnectorError): write_pages([page],sink,sink.state,"run-a",normal,budget(),batch_rows=1)
        state=sink.state;self.assertEqual(state["checkpoint"]["row_offset"],1)
        sink.commit=commit
        changed=Page([{"id":"changed"},{"id":"two"},{"id":"three"}],state["checkpoint"],page.after)
        with self.assertRaisesRegex(ConnectorError,"source_page_changed"): write_pages([changed],sink,state,"run-b",normal,budget(),batch_rows=1)
        resumed=Page(page.records,state["checkpoint"],page.after)
        result=write_pages([resumed],sink,state,"run-b",normal,budget(),batch_rows=1)
        self.assertEqual(result["records_committed"],2);self.assertEqual(len(sink.rows),3);self.assertTrue(sink.state["checkpoint"]["done"])

    def test_byte_splitting_empty_page_and_receipt_mismatch(self):
        sink=Sink();records=[{"id":str(i),"text":"x"*90000} for i in range(20)]
        page=Page(records,{"position":0,"done":False},{"position":20,"done":False})
        result=write_pages([page],sink,sink.state,"run-a",normal,budget())
        self.assertGreater(result["sequence"],1);self.assertTrue(all(len(raw)<=1<<20 for raw in sink.calls))
        empty=Page([],{"position":20,"done":False},{"position":20,"done":True})
        write_pages([empty],sink,sink.state,"run-a",normal,budget());self.assertTrue(sink.state["checkpoint"]["done"])
        bad=Sink();bad.commit=lambda raw:{"batch_id":"forged"}
        with self.assertRaisesRegex(ConnectorError,"destination_receipt_mismatch"): write_pages([empty],bad,bad.state,"run-a",normal,budget())

    def test_money_and_source_values_stay_exact(self):
        m,stream,_=self.fixture();row={"id":"setl-a","amount":123456789012345678,"currency":"INR","created_at":1790020000,"fee":17}
        result=normalize_record(m,stream,row)
        self.assertEqual(result["payload"]["fields"]["amount"],"123456789012345678")
        self.assertIs(result["payload"]["source"],row)
        with self.assertRaises(ConnectorError): normalize_record(m,stream,{**row,"amount":1.1})
        with self.assertRaises(ConnectorError): normalize_record(m,stream,{k:v for k,v in row.items() if k!="currency"})

    def test_python_wire_fixture_matches_go_receipt_hash(self):
        from decimal import Decimal
        fixture=Path(__file__).parent/"fixtures/worker-batch.json"
        raw=fixture.read_bytes();batch=json.loads(raw,parse_float=Decimal)
        expected=(fixture.with_suffix(".sha256")).read_text().strip()
        self.assertEqual(wire_batch(batch),raw)
        self.assertEqual(sha256(raw).hexdigest(),expected)

    def test_third_rest_source_executes_through_same_page_and_record_logic(self):
        m,stream,values=self.fixture()
        m["id"]="fixture/ledger";m["publisher"]["id"]="fixture";m["runtime"]["origin"]["host"]="api.example.com"
        data={"items":[{"id":"ledger-1","amount":"999999999999999999","currency":"USD","created_at":"2026-09-22T00:00:00Z"}]}
        with patch.object(BoundedSession,"_once",side_effect=[response(data),response({"items":[]})]):
            sink=Sink()
            result=write_pages(rest_pages(m,stream,values,{},budget()),sink,sink.state,"run-third",lambda row:normalize_record(m,stream,row),budget())
        self.assertEqual(result["records_committed"],1)
        self.assertEqual(sink.rows["ledger-1"]["payload"]["fields"]["amount"],"999999999999999999")
