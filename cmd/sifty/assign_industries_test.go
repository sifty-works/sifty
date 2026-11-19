package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/sifty-works/sifty-server/internal/catalog"
	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/industry"
	"github.com/sifty-works/sifty-server/internal/runner"
)

// TestAssignIndustriesAllVacanciesProcessed verifies that every vacancy in the
// database is processed and that the job reports done=total when it finishes.
// The vacancy count (207) is deliberately not a multiple of the batch size (200).
func TestAssignIndustriesAllVacanciesProcessed(t *testing.T) {
	const vacancyCount = 207 // not a multiple of 200

	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.Migrations)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	// Insert vacancies with invented titles; none match any industry keyword so
	// each ends up with industries='[]', but all must be visited.
	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	for i := 1; i <= vacancyCount; i++ {
		_, err := d.Exec(
			`INSERT INTO vacancy (identity, title, company, url, first_seen, last_seen, seen_at, industries)
			 VALUES (?,?,?,?,?,?,?,?)`,
			fmt.Sprintf("v%d", i),
			fmt.Sprintf("Administrative coordinator %d", i),
			"OfficeOrg",
			fmt.Sprintf("https://example.com/jobs/%d", i),
			now, now, "", "[]",
		)
		if err != nil {
			t.Fatalf("insert vacancy %d: %v", i, err)
		}
	}

	vocab := industry.DefaultVocab()
	vocabRef := &vocab
	cat := catalog.New(d, time.Now)

	r := runner.New(d, map[string]int{"cpu": 1}, time.Now, slog.Default())
	r.Register(runner.Kind{
		Name: "assign_industries",
		Run: func(ctx context.Context, j *runner.Job) error {
			return runAssignIndustries(ctx, j, cat, vocabRef)
		},
	})

	if _, err := r.Enqueue(runner.Spec{Kind: "assign_industries", Subject: "", Priority: 0}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Start(ctx) //nolint:errcheck

	// Poll until the job is done (up to 15 s).
	deadline := time.Now().Add(15 * time.Second)
	var job runner.Job
	for time.Now().Before(deadline) {
		if j, ok := r.Last("assign_industries"); ok && j.State == "done" {
			job = j
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if job.State != "done" {
		t.Fatalf("assign_industries job did not complete (state=%q)", job.State)
	}
	if job.Done != vacancyCount {
		t.Errorf("done: got %d, want %d", job.Done, vacancyCount)
	}
	if job.Total != vacancyCount {
		t.Errorf("total: got %d, want %d", job.Total, vacancyCount)
	}
}
