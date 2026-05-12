package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ci-system/ci/pkg/store"
)

// runRetentionLoop deletes builds (and their log files) older than
// RETENTION_DAYS (default 30). Runs every hour.
func runRetentionLoop(ctx context.Context, st store.Store, logger *slog.Logger) {
	days, err := strconv.Atoi(envOrDefault("RETENTION_DAYS", "30"))
	if err != nil || days <= 0 {
		days = 30
	}
	logger.Info("build retention enabled", "days", days)

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
			runPrune(ctx, st, cutoff, logger)
		}
	}
}

func runPrune(ctx context.Context, st store.Store, cutoff time.Time, logger *slog.Logger) {
	// Get the list of builds to prune before deleting so we can clean up logs.
	old, err := st.ListBuilds(10000)
	if err != nil {
		logger.Error("retention: list builds failed", "err", err)
		return
	}

	var toDelete []string
	for _, b := range old {
		if b.CreatedAt.Before(cutoff) {
			toDelete = append(toDelete, b.ID)
		}
	}
	if len(toDelete) == 0 {
		return
	}

	n, err := st.DeleteBuildsBefore(cutoff)
	if err != nil {
		logger.Error("retention: delete builds failed", "err", err)
		return
	}

	// Remove log files for deleted builds if LOG_DIR is set.
	if logDir := os.Getenv("LOG_DIR"); logDir != "" {
		for _, id := range toDelete {
			pattern := filepath.Join(logDir, id+"*")
			matches, _ := filepath.Glob(pattern)
			for _, f := range matches {
				if removeErr := os.Remove(f); removeErr != nil {
					logger.Warn("retention: remove log file", "file", f, "err", removeErr)
				}
			}
		}
	}

	logger.Info("retention: pruned old builds", "count", n, "cutoff", cutoff.Format(time.DateOnly))
}
