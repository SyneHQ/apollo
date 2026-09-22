"""Explicit monetary semantics; preserve Shopify MoneyBags and source identity."""
import re

from ..manifest import require
from ..records import normalize_record
from .client import at


def gid(value, kind):
    require(isinstance(value, str) and len(value) <= 128
            and re.fullmatch(f"gid://shopify/{kind}/[0-9]+", value), "source_identity_invalid")
    return value


def money(record, key):
    value = at(record, [key])
    for side in ["shopMoney", "presentmentMoney"]:
        amount, currency = at(value, [side, "amount"]), at(value, [side, "currencyCode"])
        require(isinstance(amount, str) and len(amount) <= 128
                and re.fullmatch(r"-?(0|[1-9][0-9]*)(\.[0-9]+)?", amount), "source_money_format_changed")
        require(isinstance(currency, str) and re.fullmatch(r"[A-Z]{3}", currency), "source_currency_invalid")
    return value["shopMoney"]["amount"], value["shopMoney"]["currencyCode"]


def normalize_shopify(manifest, stream, envelope):
    record, order = envelope["record"], envelope["order"]
    name = stream["id"]
    fields = {"id": gid(record.get("id"), {"orders": "Order", "line_items": "LineItem", "refunds": "Refund", "refund_transactions": "OrderTransaction"}[name])}
    if name == "orders":
        fields.update(created_at=record.get("createdAt"), updated_at=record.get("updatedAt"),
                      processed_at=record.get("processedAt"), cancelled_at=record.get("cancelledAt"),
                      financial_status=record.get("displayFinancialStatus"), currency=record.get("currencyCode"),
                      presentment_currency=record.get("presentmentCurrencyCode"))
        amounts = {"original_total": "totalPriceSet", "current_total": "currentTotalPriceSet",
                   "current_subtotal": "currentSubtotalPriceSet", "current_tax": "currentTotalTaxSet",
                   "current_discounts": "currentTotalDiscountsSet", "total_received": "totalReceivedSet",
                   "total_refunded": "totalRefundedSet", "net_payment": "netPaymentSet"}
    elif name == "line_items":
        fields.update(order_id=gid(order.get("id"), "Order"), updated_at=order.get("updatedAt"),
                      sku=record.get("sku"), title=record.get("title"))
        for source, target in [("quantity", "quantity"), ("currentQuantity", "current_quantity")]:
            value = record.get(source)
            require(type(value) is int and 0 <= value <= 2**31 - 1, "source_quantity_invalid")
            fields[target] = str(value)
        amounts = {"original_unit_amount": "originalUnitPriceSet", "discounted_total": "discountedTotalSet"}
    elif name == "refunds":
        fields.update(order_id=gid(order.get("id"), "Order"), created_at=record.get("createdAt"), updated_at=record.get("updatedAt"))
        amounts = {"recorded_amount": "totalRefundedSet"}
    else:
        refund = envelope["refund"]
        fields.update(order_id=gid(order.get("id"), "Order"), refund_id=gid(refund.get("id"), "Refund"),
                      created_at=record.get("createdAt"), updated_at=refund.get("updatedAt"),
                      processed_at=record.get("processedAt"), kind=record.get("kind"), status=record.get("status"),
                      gateway=record.get("gateway"))
        amounts = {"amount": "amountSet"}
    for target, source in amounts.items():
        amount, currency = money(record, source)
        require(fields.get("currency", currency) == currency, "source_currency_mismatch")
        fields["currency"], fields[target] = currency, amount
        if name == "orders":
            require(at(record, [source, "presentmentMoney", "currencyCode"]) == fields["presentment_currency"], "source_currency_mismatch")
    normalized = normalize_record(manifest, stream, fields)
    normalized["payload"]["source"] = envelope
    return normalized
