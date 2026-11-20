package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sifty-works/sifty-server/internal/catalog"
	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/listing"
	"github.com/sifty-works/sifty-server/internal/models"
	"github.com/sifty-works/sifty-server/internal/reading"
	"github.com/sifty-works/sifty-server/internal/runner"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock      { return &testClock{now: time.Now()} }
func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func openReadVacancyTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.Migrations)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func insertFullVacancy(t *testing.T, d *db.DB, url, title, company string) (id int64) {
	t.Helper()
	cat := catalog.New(d, func() time.Time { return time.Now() })
	body := "<p>Join our " + title + " team at " + company + ". Full-time position with competitive salary. The role involves building distributed systems and collaborating with cross-functional teams.</p>"
	id, _, err := cat.Upsert(context.Background(), listing.Posting{
		URL:       url,
		Title:     title,
		Company:   company,
		Text:      body,
		TextState: listing.TextFull,
	}, "src")
	if err != nil {
		t.Fatalf("upsert vacancy: %v", err)
	}
	return id
}

func emptyReadingReply() string {
	out := map[string]any{"requirements": []any{}, "conditions": map[string]any{}}
	b, _ := json.Marshal(out)
	return string(b)
}

func runReadVacancyDirect(t *testing.T, d *db.DB, vacancyID, version int64, m models.Model) ([]runner.Spec, error) {
	t.Helper()
	var specs []runner.Spec
	j := &runner.Job{Subject: fmt.Sprintf("%d/%d", vacancyID, version)}
	err := runReadVacancy(
		context.Background(),
		j,
		d,
		func(_ int64) models.Model { return m },
		func(s runner.Spec) (int64, error) {
			specs = append(specs, s)
			return 0, nil
		},
	)
	return specs, err
}

func registerReadVacancyKind(r *runner.Runner, d *db.DB, m *models.FakeModel) {
	r.Register(runner.Kind{
		Name:   "read_vacancy",
		Resume: true,
		Serial: true,
		Run: func(runCtx context.Context, j *runner.Job) error {
			return runReadVacancy(runCtx, j, d, func(_ int64) models.Model { return m }, r.Enqueue)
		},
	})
}

func waitForJobState(t *testing.T, r *runner.Runner, kind, state string, timeout time.Duration) runner.Job {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if j, ok := r.Last(kind); ok && j.State == state {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %q did not reach state %q within %v", kind, state, timeout)
	return runner.Job{}
}

func TestReadVacancy_reads_vacancy_and_saves_reading(t *testing.T) {
	d := openReadVacancyTestDB(t)
	id := insertFullVacancy(t, d, "https://jobs.example.com/rv1", "Backend Engineer", "Stackworks")

	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: emptyReadingReply()})

	specs, err := runReadVacancyDirect(t, d, id, 1, fm)
	if err != nil {
		t.Fatalf("runReadVacancy: %v", err)
	}

	var readerVer string
	d.QueryRow(`SELECT reader_version FROM vacancy_reading WHERE vacancy_id=?`, id).Scan(&readerVer)
	if readerVer == "" {
		t.Error("reading was not saved to vacancy_reading")
	}
	if readerVer == "import-1" {
		t.Error("reader_version should not be import-1")
	}

	if len(specs) != 1 || specs[0].Kind != "recompute_fit" {
		t.Errorf("expected recompute_fit enqueued, got %v", specs)
	}
}

func TestReadVacancy_deleted_vacancy_ends_without_error(t *testing.T) {
	d := openReadVacancyTestDB(t)
	id := insertFullVacancy(t, d, "https://jobs.example.com/rv2", "Platform Engineer", "Stackworks")
	d.Exec(`DELETE FROM vacancy WHERE id=?`, id)

	fm := &models.FakeModel{}
	specs, err := runReadVacancyDirect(t, d, id, 1, fm)
	if err != nil {
		t.Fatalf("expected no error when vacancy deleted, got: %v", err)
	}
	if len(specs) != 0 {
		t.Errorf("expected no specs enqueued, got %v", specs)
	}
	if len(fm.Calls()) != 0 {
		t.Errorf("model should not be called for deleted vacancy")
	}
}

func TestReadVacancy_version_mismatch_ends_without_error(t *testing.T) {
	d := openReadVacancyTestDB(t)
	id := insertFullVacancy(t, d, "https://jobs.example.com/rv3", "Platform Engineer", "Stackworks")

	fm := &models.FakeModel{}
	specs, err := runReadVacancyDirect(t, d, id, 99, fm)
	if err != nil {
		t.Fatalf("expected no error on version mismatch, got: %v", err)
	}
	if len(specs) != 0 {
		t.Errorf("expected no specs enqueued, got %v", specs)
	}
}

func TestReadVacancy_wait_error_requeues_without_counting_attempt(t *testing.T) {
	d := openReadVacancyTestDB(t)
	id := insertFullVacancy(t, d, "https://jobs.example.com/rv4", "Data Engineer", "Stackworks")

	retryAt := time.Now().Add(10 * time.Minute).Truncate(time.Second)
	fm := &models.FakeModel{}
	fm.AddError(models.WaitError{RetryAfter: retryAt})
	fm.AddReply(models.Response{Text: emptyReadingReply()})

	r := runner.New(d, nil, time.Now, slog.Default())
	registerReadVacancyKind(r, d, fm)

	r.Enqueue(runner.Spec{Kind: "read_vacancy", Subject: fmt.Sprintf("%d/1", id)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Start(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := r.Last("read_vacancy"); ok && j.State == "queued" && j.Error != "" {
			if j.Attempts != 0 {
				t.Errorf("wait error should not count as attempt: attempts=%d", j.Attempts)
			}
			nb := j.NotBefore.UTC().Truncate(time.Second)
			want := retryAt.UTC().Truncate(time.Second)
			if !nb.Equal(want) {
				t.Errorf("not_before = %v, want %v", nb, want)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job did not go back to queued after WaitError")
}

func TestReadVacancy_paid_bulk_refused_leaves_job_waiting(t *testing.T) {
	d := openReadVacancyTestDB(t)
	id := insertFullVacancy(t, d, "https://jobs.example.com/rv5", "DevOps Engineer", "Stackworks")

	fm := &models.FakeModel{}
	fm.AddError(models.ErrBulkOnPaid)
	fm.AddReply(models.Response{Text: emptyReadingReply()})

	r := runner.New(d, nil, time.Now, slog.Default())
	registerReadVacancyKind(r, d, fm)

	r.Enqueue(runner.Spec{Kind: "read_vacancy", Subject: fmt.Sprintf("%d/1", id)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Start(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := r.Last("read_vacancy"); ok && j.State == "queued" && j.Error != "" {
			if j.Attempts != 0 {
				t.Errorf("paid bulk refused should not count as attempt: attempts=%d", j.Attempts)
			}
			if !j.NotBefore.After(time.Now()) {
				t.Errorf("not_before should be in the future, got %v", j.NotBefore)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job did not go back to queued after ErrBulkOnPaid")
}

func TestReadVacancy_three_model_errors_fail_job(t *testing.T) {
	d := openReadVacancyTestDB(t)
	id := insertFullVacancy(t, d, "https://jobs.example.com/rv6", "Frontend Engineer", "Stackworks")

	fm := &models.FakeModel{}
	fm.AddError(errors.New("model error"))
	fm.AddError(errors.New("model error"))
	fm.AddError(errors.New("model error"))

	clk := newTestClock()
	r := runner.New(d, nil, clk.Now, slog.Default())
	registerReadVacancyKind(r, d, fm)

	r.Enqueue(runner.Spec{Kind: "read_vacancy", Subject: fmt.Sprintf("%d/1", id)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Start(ctx)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Millisecond):
				clk.Advance(5 * time.Minute)
			}
		}
	}()

	j := waitForJobState(t, r, "read_vacancy", "failed", 5*time.Second)
	if j.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", j.Attempts)
	}
}

func TestReadVacancy_two_jobs_run_one_after_the_other(t *testing.T) {
	d := openReadVacancyTestDB(t)
	id1 := insertFullVacancy(t, d, "https://jobs.example.com/rv7a", "Senior Engineer", "Alpha")
	id2 := insertFullVacancy(t, d, "https://jobs.example.com/rv7b", "Lead Engineer", "Beta")

	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: emptyReadingReply()})
	fm.AddReply(models.Response{Text: emptyReadingReply()})

	var maxConcurrent int
	var current int
	var mu sync.Mutex

	r := runner.New(d, nil, time.Now, slog.Default())
	r.Register(runner.Kind{
		Name:   "read_vacancy",
		Resume: true,
		Serial: true,
		Run: func(runCtx context.Context, j *runner.Job) error {
			mu.Lock()
			current++
			if current > maxConcurrent {
				maxConcurrent = current
			}
			mu.Unlock()
			err := runReadVacancy(runCtx, j, d, func(_ int64) models.Model { return fm }, r.Enqueue)
			mu.Lock()
			current--
			mu.Unlock()
			return err
		},
	})

	r.Enqueue(runner.Spec{Kind: "read_vacancy", Subject: fmt.Sprintf("%d/1", id1)})
	r.Enqueue(runner.Spec{Kind: "read_vacancy", Subject: fmt.Sprintf("%d/1", id2)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Start(ctx)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		calls := len(fm.Calls())
		if calls >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	max := maxConcurrent
	mu.Unlock()
	if max > 1 {
		t.Errorf("max concurrent = %d, want 1 (serial)", max)
	}
}

func TestReadVacancy_vacancy_deleted_while_reading_saves_nothing(t *testing.T) {
	d := openReadVacancyTestDB(t)
	id := insertFullVacancy(t, d, "https://jobs.example.com/rv10", "Systems Engineer", "Stackworks")

	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: emptyReadingReply()})

	var specs []runner.Spec
	j := &runner.Job{Subject: fmt.Sprintf("%d/1", id)}
	var capturedVacancyID int64
	err := runReadVacancy(
		context.Background(),
		j,
		d,
		func(vid int64) models.Model {
			capturedVacancyID = vid
			d.Exec(`DELETE FROM vacancy WHERE id=?`, id)
			return fm
		},
		func(s runner.Spec) (int64, error) {
			specs = append(specs, s)
			return 0, nil
		},
	)
	if err != nil {
		t.Fatalf("expected no error when vacancy deleted before save, got: %v", err)
	}
	if len(specs) != 0 {
		t.Errorf("expected no recompute_fit queued when vacancy deleted, got %v", specs)
	}
	if capturedVacancyID != id {
		t.Errorf("model fn received vacancyID=%d, want %d", capturedVacancyID, id)
	}
	var readingCount int
	d.QueryRow(`SELECT count(*) FROM vacancy_reading WHERE vacancy_id=?`, id).Scan(&readingCount)
	if readingCount != 0 {
		t.Errorf("expected no reading saved when vacancy deleted, got count=%d", readingCount)
	}
}

func TestReadVacancy_saves_reading_for_correct_version(t *testing.T) {
	d := openReadVacancyTestDB(t)
	id := insertFullVacancy(t, d, "https://jobs.example.com/rv12", "Cloud Engineer", "Stackworks")

	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: emptyReadingReply()})

	_, err := runReadVacancyDirect(t, d, id, 1, fm)
	if err != nil {
		t.Fatalf("runReadVacancy: %v", err)
	}

	var savedVersion int64
	d.QueryRow(`SELECT version FROM vacancy_reading WHERE vacancy_id=?`, id).Scan(&savedVersion)
	if savedVersion != 1 {
		t.Errorf("reading version = %d, want 1", savedVersion)
	}
}

func TestReadVacancy_reading_has_correct_reader_version(t *testing.T) {
	d := openReadVacancyTestDB(t)
	id := insertFullVacancy(t, d, "https://jobs.example.com/rv13", "Site Reliability Engineer", "Stackworks")

	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: emptyReadingReply()})

	_, err := runReadVacancyDirect(t, d, id, 1, fm)
	if err != nil {
		t.Fatalf("runReadVacancy: %v", err)
	}

	var savedReaderVer string
	d.QueryRow(`SELECT reader_version FROM vacancy_reading WHERE vacancy_id=?`, id).Scan(&savedReaderVer)
	if savedReaderVer != reading.ReaderVersion() {
		t.Errorf("reader_version = %q, want %q", savedReaderVer, reading.ReaderVersion())
	}
}
