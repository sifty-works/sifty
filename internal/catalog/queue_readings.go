package catalog

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sifty-works/sifty-server/internal/db"
)

const lowReadingPriority = -1

type ReadingCandidate struct {
	VacancyID int64
	Version   int64
	Priority  int
}

func ReadingCandidates(ctx context.Context, database *db.DB, visSQL string, visArgs []any, readerVersion string) ([]ReadingCandidate, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	q := `SELECT v.id, v.version, r.reader_version IS NULL
	      FROM vacancy v
	      LEFT JOIN vacancy_reading r ON r.vacancy_id=v.id AND r.version=v.version
	      WHERE v.text_state='full'
	      AND ` + NotExcluded + `
	      AND (v.expires_at='' OR v.expires_at>?)
	      AND ` + visSQL + `
	      AND COALESCE(r.reader_version,'') != 'import-1'
	      AND (r.reader_version IS NULL OR r.reader_version != ?)
	      AND NOT EXISTS (SELECT 1 FROM job j WHERE j.kind='read_vacancy' AND j.subject=(v.id||'/'||v.version) AND j.state IN ('queued','running','failed'))
	      ORDER BY v.first_seen ASC, v.id ASC`

	args := append(append([]any{now}, visArgs...), readerVersion)

	rows, err := database.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("catalog: reading candidates: %w", err)
	}
	defer rows.Close()

	var out []ReadingCandidate
	for rows.Next() {
		var c ReadingCandidate
		var isNew bool
		if err := rows.Scan(&c.VacancyID, &c.Version, &isNew); err != nil {
			return nil, fmt.Errorf("catalog: reading candidates: scan: %w", err)
		}
		if !isNew {
			c.Priority = lowReadingPriority
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func IsFKError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}
