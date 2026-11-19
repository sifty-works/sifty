package catalog

import (
	"context"
	"fmt"
	"time"
)

// ExpiredWithoutApplication returns at most limit IDs of vacancies whose
// expires_at is not empty, is before cutoff, and have no application row.
// Callers that delete while iterating must pass offset 0 each time; the
// deleted rows do not shift the result set.
func (c *Catalog) ExpiredWithoutApplication(ctx context.Context, cutoff time.Time, limit int) ([]int64, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id FROM vacancy
		WHERE expires_at != '' AND expires_at < ?
		  AND NOT EXISTS (SELECT 1 FROM application WHERE vacancy = vacancy.id)
		ORDER BY id
		LIMIT ?`,
		cutoff.UTC().Format(time.RFC3339), limit)
	if err != nil {
		return nil, fmt.Errorf("catalog: expired without app: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("catalog: expired without app: scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CountExpiredWithoutApplication returns the number of vacancies whose
// expires_at is not empty, is before cutoff, and have no application row.
func (c *Catalog) CountExpiredWithoutApplication(ctx context.Context, cutoff time.Time) (int, error) {
	var n int
	err := c.db.QueryRowContext(ctx, `
		SELECT count(*) FROM vacancy
		WHERE expires_at != '' AND expires_at < ?
		  AND NOT EXISTS (SELECT 1 FROM application WHERE vacancy = vacancy.id)`,
		cutoff.UTC().Format(time.RFC3339)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("catalog: count expired without app: %w", err)
	}
	return n, nil
}
