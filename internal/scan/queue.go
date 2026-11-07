package scan

import (
	"encoding/json"

	"quipster/internal/store"
)

// EnqueueScanJob builds the scan job payload for a root-scoped or full scan.
func EnqueueScanJob(st *store.Store, roots []string, prune bool) (int64, error) {
	payload, err := json.Marshal(map[string]any{"roots": roots, "prune": prune})
	if err != nil {
		return 0, err
	}
	return st.EnqueueJob("scan", string(payload), 3)
}
