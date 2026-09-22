"""Map known failures to a finite public vocabulary without source content."""
from .manifest import ConnectorError

GROUPS = {
    "source_access_required": {"shopify_read_orders_required", "shopify_history_scope_required"},
    "source_access_denied": {"source_http_401", "source_http_403"},
    "source_changed": {"shopify_parent_changed", "shopify_window_changed", "source_page_changed"},
    "source_limit": {"shopify_dense_window", "shopify_child_pagination_limit", "shopify_pagination_limit", "source_page_budget", "response_limit", "record_limit"},
    "source_version_changed": {"shopify_api_version_changed", "connector_upgrade_required"},
    "source_schema_changed": {"source_schema_changed", "source_required_field_missing", "source_field_type_changed", "source_identity_invalid",
        "source_money_format_changed", "source_currency_invalid", "source_currency_mismatch", "source_quantity_invalid", "source_timestamp_invalid",
        "source_timestamp_timezone_missing", "source_cursor_invalid", "source_cursor_missing", "source_cursor_loop", "duplicate_source_id", "invalid_source_json"},
    "source_unavailable": {"source_dns_failed", "source_connect_failed", "source_transport_failed", "source_retry_exhausted", "source_retry_deferred", "shopify_graphql_failed"},
    "sync_timeout": {"deadline_exceeded"},
    "sync_cancelled": {"cancelled"},
    "source_configuration_invalid": {"invalid_configuration", "missing_configuration", "shopify_history_in_future"},
    "sync_checkpoint_invalid": {"checkpoint_invalid"},
    "destination_ack_unknown": {"destination_ack_unknown"},
    "destination_result_invalid": {"destination_receipt_mismatch", "destination_not_complete"},
}


def public_failure(error):
    if isinstance(error, ConnectorError):
        for code, causes in GROUPS.items():
            if str(error) in causes:
                return code
    return "connector_worker_failed"
