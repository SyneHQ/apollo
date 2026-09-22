"""Pinned read operations; no query text comes from a manifest or user prompt."""
API_VERSION = "2026-07"
ENDPOINT = f"/admin/api/{API_VERSION}/graphql.json"
MONEY = "{ shopMoney { amount currencyCode } presentmentMoney { amount currencyCode } }"
PAGE_INFO = "pageInfo { hasNextPage endCursor }"
ORDER_FIELDS = """id createdAt updatedAt processedAt cancelledAt displayFinancialStatus
currencyCode presentmentCurrencyCode """ + " ".join(
    field + MONEY for field in ["totalPriceSet", "currentTotalPriceSet", "currentSubtotalPriceSet",
                               "currentTotalTaxSet", "currentTotalDiscountsSet", "totalReceivedSet",
                               "totalRefundedSet", "netPaymentSet"])
REFUND_FIELDS = "id createdAt updatedAt totalRefundedSet" + MONEY
LINE_FIELDS = "id sku title quantity currentQuantity originalUnitPriceSet" + MONEY + " discountedTotalSet" + MONEY
TRANSACTION_FIELDS = "id createdAt processedAt kind status gateway amountSet" + MONEY


def orders_query(name, fields):
    return (f"query {name}($first: Int!, $after: String, $filter: String!) {{ "
            "orders(first: $first, after: $after, query: $filter, sortKey: UPDATED_AT) { nodes { "
            + fields + " } " + PAGE_INFO + " } }")


QUERIES = {
    "access": "query SyneAccess { currentAppInstallation { accessScopes { handle } } }",
    "orders": orders_query("SyneOrders", ORDER_FIELDS),
    "order_ids": orders_query("SyneOrderIds", "id createdAt updatedAt"),
    "refunds": orders_query("SyneRefunds", "id createdAt updatedAt refunds { " + REFUND_FIELDS + " }"),
    "line_items": "query SyneLineItems($id: ID!, $first: Int!, $after: String) { order(id: $id) { id updatedAt lineItems(first: $first, after: $after) { nodes { " + LINE_FIELDS + " } " + PAGE_INFO + " } } }",
    "refund_transactions": "query SyneRefundTransactions($id: ID!, $first: Int!, $after: String) { refund(id: $id) { id updatedAt order { id } transactions(first: $first, after: $after) { nodes { " + TRANSACTION_FIELDS + " } " + PAGE_INFO + " } } }",
}
