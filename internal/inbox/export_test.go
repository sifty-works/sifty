package inbox

import (
	"context"
	"time"

	"github.com/sifty-works/sifty/internal/db"
)

// NewRepoForTest exposes the internal repo constructor for measure tests.
func NewRepoForTest(database *db.DB) *repo { return newRepo(database) }

// ListForTest exposes the internal list function for measure tests.
func ListForTest(ctx context.Context, r *repo, now func() time.Time, q string) ([]Row, int, error) {
	return list(ctx, r, now, q)
}
