package connectorjobs

import (
	"encoding/json"
	"io"
	"strings"
)

var workerFailureCodes = map[string]bool{
	"source_access_required": true, "source_access_denied": true,
	"source_changed": true, "source_limit": true, "source_version_changed": true,
	"source_schema_changed": true, "source_unavailable": true,
	"sync_timeout": true, "sync_cancelled": true, "source_configuration_invalid": true,
	"sync_checkpoint_invalid": true, "destination_ack_unknown": true,
	"destination_result_invalid": true, "connector_worker_failed": true,
}

// Only a bounded, run-bound report and a constant vocabulary can cross from a
// failed process into metadata. Unknown codes and arbitrary log text are ignored.
func workerFailure(output, runID string) string {
	if len(output) > 512 {
		return ""
	}
	var result struct {
		Status string `json:"status"`
		RunID  string `json:"run_id"`
		Error  string `json:"error"`
	}
	d := json.NewDecoder(strings.NewReader(output))
	d.DisallowUnknownFields()
	if err := d.Decode(&result); err != nil {
		return ""
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF || result.Status != "failed" || result.RunID != runID || !workerFailureCodes[result.Error] {
		return ""
	}
	return result.Error
}
