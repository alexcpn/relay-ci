package main

import (
	"encoding/json"
	"net/http"

	"github.com/ci-system/ci/pkg/logstore"
)

// handleLogsHTTP serves logs via HTTP for easy viewing.
// GET /logs?task_id=<id>&offset=0&limit=100
func handleLogsHTTP(w http.ResponseWriter, r *http.Request, store *logstore.Store) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		// Return task list with line counts.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"usage": "GET /logs?task_id=<task-id>&offset=0&limit=100",
		})
		return
	}

	offset := int64(0)
	limit := int64(0) // 0 = all

	if v := r.URL.Query().Get("offset"); v != "" {
		var n int64
		if _, err := json.Number(v).Int64(); err == nil {
			offset = n
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		var n int64
		if _, err := json.Number(v).Int64(); err == nil {
			limit = n
		}
	}

	lines, total := store.Get(taskID, offset, limit)

	type logLine struct {
		Line    int64  `json:"line"`
		Content string `json:"content"`
		Stream  string `json:"stream"`
	}

	result := struct {
		TaskID string    `json:"task_id"`
		Total  int64     `json:"total_lines"`
		Lines  []logLine `json:"lines"`
	}{
		TaskID: taskID,
		Total:  total,
		Lines:  make([]logLine, len(lines)),
	}

	for i, l := range lines {
		stream := "stdout"
		switch l.Stream {
		case logstore.StreamStderr:
			stream = "stderr"
		case logstore.StreamSystem:
			stream = "system"
		}
		result.Lines[i] = logLine{
			Line:    l.LineNumber,
			Content: l.Content,
			Stream:  stream,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
