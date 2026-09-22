"""Read a verified workspace CSV without accepting paths or remote URLs."""
import csv
from hashlib import sha256
import io
import re
import tempfile

from .manifest import ConnectorError, configuration, require
from .pages import Page, source_bytes

MAX_FILE_BYTES = 50 * 1024 * 1024


def csv_pages(manifest, values, source, expected_hash, checkpoint, budget):
    """source is an already authorized binary stream, not a caller-supplied path.

    Copy once to a private temporary file and verify its granted content hash.
    The parser only reads this immutable copy and never evaluates spreadsheet
    formulas. File access/retention policy belongs to the grant and runner.
    """
    require(manifest["runtime"]["kind"] == "file" and manifest["runtime"]["formats"] == ["csv"], "file_format_unsupported")
    values = configuration(manifest, values)
    require(isinstance(expected_hash, str) and re.fullmatch(r"[a-f0-9]{64}", expected_hash), "file_hash_required")
    position = 0 if not checkpoint or checkpoint.get("done") is True else checkpoint.get("position")
    require(type(position) is int and position >= 0, "checkpoint_invalid")
    with tempfile.TemporaryFile(mode="w+b") as verified:
        digest, size = sha256(), 0
        while True:
            budget.remaining()
            chunk = source.read(65536)
            if not chunk:
                break
            size += len(chunk)
            require(size <= MAX_FILE_BYTES, "file_limit")
            digest.update(chunk); verified.write(chunk)
        require(digest.hexdigest() == expected_hash, "file_changed")
        verified.seek(0)
        # csv's field-size limit is process global. Keep the standard bound;
        # isolated workers do not increase it based on untrusted files.
        text = io.TextIOWrapper(verified, encoding="utf-8-sig", newline="")
        try:
            reader = csv.DictReader(text, strict=True)
            headers = reader.fieldnames
            require(headers and len(headers) <= 200 and len(headers) == len(set(headers))
                    and all(h and len(h) <= 256 for h in headers), "file_headers_invalid")
            mapping = {key: values[key+"_column"] for key in ["id", "amount", "currency"]}
            require(len(set(mapping.values())) == 3 and all(column in headers for column in mapping.values()), "file_mapping_invalid")
            records, bytes_used, row_index = [], 0, 0
            start = {"position": position, "done": False, **({k:v for k,v in checkpoint.items() if k in {"page_hash","row_offset"}} if checkpoint and not checkpoint.get("done") else {})}
            for row in reader:
                budget.remaining()
                require(None not in row and all(value is not None for value in row.values()), "file_row_shape_changed")
                row_index += 1
                if row_index <= position:
                    continue
                record = {key:row[column] for key,column in mapping.items()}
                require(re.fullmatch(r"[A-Z]{3}", record["currency"]), "file_currency_invalid")
                record["payload"] = row
                row_bytes = len(source_bytes(record))
                require(row_bytes <= 128 * 1024, "record_limit")
                if records and (len(records) >= manifest["limits"]["batchRows"] or bytes_used+row_bytes > manifest["limits"]["responseBytes"]):
                    after = {"position":row_index-1, "done":False}
                    yield Page(records, start, after)
                    start, records, bytes_used = after, [], 0
                records.append(record); bytes_used += row_bytes
            require(row_index >= position, "checkpoint_invalid")
            yield Page(records, start, {"position":row_index, "done":True})
        except (UnicodeError, csv.Error):
            raise ConnectorError("file_parse_failed") from None
        finally:
            text.detach()
