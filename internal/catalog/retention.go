package catalog

import (
	"context"
	"fmt"
	"time"
)

func (c *Catalog) RetainableExpired(ctx context.Context, cutoff time.Time) ([]int64, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id FROM vacancy
		WHERE expires_at != '' AND expires_at < ?
		  AND NOT EXISTS (SELECT 1 FROM application WHERE vacancy = vacancy.id)
		ORDER BY id`,
		cutoff.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("catalog: retainable expired: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("catalog: retainable expired: scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
