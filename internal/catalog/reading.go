package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/reading"
)

func SaveReading(ctx context.Context, database *db.DB, vacancyID, version int64, readerVersion string, rec reading.Record) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("catalog: save reading: marshal: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = database.ExecContext(ctx,
		`INSERT OR REPLACE INTO vacancy_reading (vacancy_id, version, reader_version, record, created)
		 VALUES (?, ?, ?, ?, ?)`,
		vacancyID, version, readerVersion, string(b), now)
	if err != nil {
		return fmt.Errorf("catalog: save reading %d: %w", vacancyID, err)
	}
	return nil
}
