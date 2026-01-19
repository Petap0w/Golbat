package db

import (
	"context"

	"github.com/jmoiron/sqlx"
)

// BatchUpsertWithResults processes items individually and returns which indices succeeded
// This allows ACKing successful items even if some fail
func BatchUpsertWithResults(ctx context.Context, db *sqlx.DB, query string, items interface{}, metricName string) ([]int, error) {
	slice, ok := items.([]interface{})
	if !ok {
		return nil, nil
	}

	successIndices := make([]int, 0, len(slice))

	for i, item := range slice {
		_, err := db.NamedExecContext(ctx, query, item)
		if err == nil {
			successIndices = append(successIndices, i)
		}
		// Continue processing even on error - don't want to lose the whole batch
	}

	if statsCollector != nil && len(successIndices) > 0 {
		statsCollector.IncDbQuery(metricName, nil)
	}

	return successIndices, nil
}

