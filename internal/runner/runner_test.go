package runner_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sifty-works/sifty/internal/db"
	"github.com/sifty-works/sifty/internal/runner"
)

var base = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

func openDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.Migrations)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock(t time.Time) *clock        { return &clock{now: t} }
func (c *clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }
func (c *clock) Set(t time.Time)         { c.mu.Lock(); c.now = t; c.mu.Unlock() }

func newRunner(d *db.DB, lanes map[string]int, cl *clock) *runner.Runner {
	now := func() time.Time { return base }
	if cl != nil {
		now = cl.Now
	}
	return runner.New(d, lanes, now, slog.Default())
}

func startRunner(t *testing.T, r *runner.Runner) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := r.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("runner.Start: %v", err)
		}
	}()
	return cancel
}

func waitEvent(t *testing.T, ch <-chan runner.Event, pred func(runner.Event) bool) runner.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			if pred(ev) {
				return ev
			}
		case <-deadline:
			t.Fatal("timed out waiting for event")
			return runner.Event{}
		}
	}
}

// --- dispatch and deduplication ---

func TestDispatchRunsAllQueuedJobs(t *testing.T) {
	d := openDB(t)
	r := newRunner(d, nil, nil)
	r.Register(runner.Kind{Name: "noop", Run: func(_ context.Context, _ *runner.Job) error { return nil }})
	for _, s := range []string{"a", "b", "c"} {
		r.Enqueue(runner.Spec{Kind: "noop", Subject: s}) //nolint:errcheck
	}
	ch, unsub := r.Subscribe()
	defer unsub()
	cancel := startRunner(t, r)
	defer cancel()
	done := map[string]bool{}
	for len(done) < 3 {
		ev := waitEvent(t, ch, func(e runner.Event) bool { return e.Job.State == "done" })
		done[ev.Job.Subject] = true
	}
}

func TestFutureNotBeforeJobWaits(t *testing.T) {
	d := openDB(t)
	cl := newClock(base)
	ran := make(chan string, 2)
	r := newRunner(d, nil, cl)
	r.Register(runner.Kind{Name: "nb", Run: func(_ context.Context, j *runner.Job) error { ran <- j.Subject; return nil }})
	r.Enqueue(runner.Spec{Kind: "nb", Subject: "ready"}) //nolint:errcheck
	cl.Advance(time.Hour)
	r.Enqueue(runner.Spec{Kind: "nb", Subject: "future"}) //nolint:errcheck
	cl.Set(base.Add(30 * time.Minute))
	cancel := startRunner(t, r)
	defer cancel()
	select {
	case s := <-ran:
		if s != "ready" {
			t.Errorf("first run: got %q, want ready", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ready job did not run")
	}
	var state string
	d.QueryRow(`SELECT state FROM job WHERE subject='future'`).Scan(&state) //nolint:errcheck
	if state != "queued" {
		t.Errorf("future job state: got %q, want queued", state)
	}
}

func TestDeduplicationReturnsExistingID(t *testing.T) {
	d := openDB(t)
	hold := make(chan struct{})
	r := newRunner(d, nil, nil)
	r.Register(runner.Kind{Name: "dedup", Run: func(ctx context.Context, _ *runner.Job) error { <-hold; return nil }})
	id1, _ := r.Enqueue(runner.Spec{Kind: "dedup", Subject: "x"})
	if id2, _ := r.Enqueue(runner.Spec{Kind: "dedup", Subject: "x"}); id1 != id2 {
		t.Fatalf("dedup while queued: got %d, want %d", id2, id1)
	}
	ch, unsub := r.Subscribe()
	defer unsub()
	cancel := startRunner(t, r)
	defer cancel()
	waitEvent(t, ch, func(e runner.Event) bool { return e.Job.State == "running" })
	if id3, _ := r.Enqueue(runner.Spec{Kind: "dedup", Subject: "x"}); id1 != id3 {
		t.Fatalf("dedup while running: got %d, want %d", id3, id1)
	}
	close(hold)
	waitEvent(t, ch, func(e runner.Event) bool { return e.Job.State == "done" })
	if id4, _ := r.Enqueue(runner.Spec{Kind: "dedup", Subject: "x"}); id4 == id1 {
		t.Error("after done: expected new id, got same")
	}
}

func TestUnregisteredKindStaysQueued(t *testing.T) {
	d := openDB(t)
	ts := base.UTC().Format(time.RFC3339)
	_, err := d.Exec(
		`INSERT INTO job (kind,subject,priority,state,attempts,not_before,progress,error,params,created,updated)
		 VALUES ('unknown','x',0,'queued',0,?,'{}','',NULL,?,?)`, ts, ts, ts,
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	noop := make(chan struct{})
	r := newRunner(d, nil, nil)
	r.Register(runner.Kind{Name: "noop", Run: func(_ context.Context, _ *runner.Job) error { close(noop); return nil }})
	r.Enqueue(runner.Spec{Kind: "noop", Subject: "1"}) //nolint:errcheck
	cancel := startRunner(t, r)
	defer cancel()
	select {
	case <-noop:
	case <-time.After(2 * time.Second):
		t.Fatal("noop job did not run")
	}
	var state string
	d.QueryRow(`SELECT state FROM job WHERE kind='unknown'`).Scan(&state) //nolint:errcheck
	if state != "queued" {
		t.Errorf("unregistered kind state: got %q, want queued", state)
	}
}

// --- lanes ---

func TestLaneLimitsParallelism(t *testing.T) {
	d := openDB(t)
	acquired := make(chan struct{}, 3)
	proceed := make(chan struct{})
	r := newRunner(d, map[string]int{"net": 2}, nil)
	r.Register(runner.Kind{Name: "net-job", Run: func(ctx context.Context, j *runner.Job) error {
		rel, err := j.Lane(ctx, "net")
		if err != nil {
			return err
		}
		defer rel()
		acquired <- struct{}{}
		select {
		case <-proceed:
		case <-ctx.Done():
		}
		return nil
	}})
	for i := 0; i < 3; i++ {
		r.Enqueue(runner.Spec{Kind: "net-job", Subject: fmt.Sprintf("%d", i)}) //nolint:errcheck
	}
	cancel := startRunner(t, r)
	defer cancel()
	<-acquired
	<-acquired
	select {
	case <-acquired:
		t.Fatal("3rd job acquired lane before first two released")
	default:
	}
	close(proceed)
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("3rd job never acquired lane after release")
	}
}

func TestJobWaitingForLaneDoesNotBlockOtherLane(t *testing.T) {
	d := openDB(t)
	holdNet := make(chan struct{})
	cpuDone := make(chan struct{})
	r := newRunner(d, map[string]int{"net": 1, "cpu": 1}, nil)
	r.Register(runner.Kind{Name: "net-job", Run: func(ctx context.Context, j *runner.Job) error {
		rel, _ := j.Lane(ctx, "net")
		defer rel()
		select {
		case <-holdNet:
		case <-ctx.Done():
		}
		return nil
	}})
	r.Register(runner.Kind{Name: "cpu-job", Run: func(ctx context.Context, j *runner.Job) error {
		rel, _ := j.Lane(ctx, "cpu")
		defer rel()
		close(cpuDone)
		return nil
	}})
	r.Enqueue(runner.Spec{Kind: "net-job", Subject: "1", Priority: 1}) //nolint:errcheck
	r.Enqueue(runner.Spec{Kind: "cpu-job", Subject: "1"})              //nolint:errcheck
	cancel := startRunner(t, r)
	defer cancel()
	select {
	case <-cpuDone:
	case <-time.After(2 * time.Second):
		t.Fatal("cpu job blocked by net lane")
	}
	close(holdNet)
}

// --- subscribe, progress ---

func TestSubscribeAndProgress(t *testing.T) {
	d := openDB(t)
	r := newRunner(d, nil, nil)
	r.Register(runner.Kind{Name: "prog", Run: func(_ context.Context, j *runner.Job) error {
		j.Progress(1, 10, "step-a")
		return nil
	}})
	ch, unsub := r.Subscribe()
	defer unsub()
	r.Enqueue(runner.Spec{Kind: "prog", Subject: "1"}) //nolint:errcheck
	cancel := startRunner(t, r)
	defer cancel()
	var sawRunning, sawProgress bool
	waitEvent(t, ch, func(e runner.Event) bool {
		if e.Job.State == "running" {
			sawRunning = true
		}
		if e.Job.Total > 0 {
			sawProgress = true
		}
		return e.Job.State == "done"
	})
	if !sawRunning {
		t.Error("running state not observed")
	}
	if !sawProgress {
		t.Error("no progress event received")
	}
}

func TestUnsubscribeTwiceDoesNotPanic(t *testing.T) {
	_, unsub := newRunner(openDB(t), nil, nil).Subscribe()
	unsub()
	unsub() // must not panic
}

// --- retries, permanent, panic ---

func TestRetryNotBeforeIsSetCorrectly(t *testing.T) {
	d := openDB(t)
	cl := newClock(base)
	var attempts atomic.Int32
	done := make(chan struct{})
	r := newRunner(d, nil, cl)
	r.Register(runner.Kind{Name: "fail", Run: func(_ context.Context, _ *runner.Job) error {
		if attempts.Add(1) == 1 {
			close(done)
		}
		return errors.New("transient")
	}})
	id, _ := r.Enqueue(runner.Spec{Kind: "fail", Subject: "1"})
	cancel := startRunner(t, r)
	defer cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("first attempt did not run")
	}
	time.Sleep(50 * time.Millisecond)
	var notBefore string
	d.QueryRow(`SELECT not_before FROM job WHERE id=?`, id).Scan(&notBefore) //nolint:errcheck
	want := base.Add(30 * time.Second).UTC().Format(time.RFC3339)
	if notBefore != want {
		t.Errorf("not_before: got %q, want %q", notBefore, want)
	}
}

func TestPermanentErrorFailsImmediately(t *testing.T) {
	d := openDB(t)
	r := newRunner(d, nil, nil)
	r.Register(runner.Kind{Name: "perm", Run: func(_ context.Context, _ *runner.Job) error {
		return runner.Permanent(errors.New("done"))
	}})
	ch, unsub := r.Subscribe()
	defer unsub()
	r.Enqueue(runner.Spec{Kind: "perm", Subject: "1"}) //nolint:errcheck
	cancel := startRunner(t, r)
	defer cancel()
	ev := waitEvent(t, ch, func(e runner.Event) bool { return e.Job.Kind == "perm" && e.Job.State == "failed" })
	if ev.Job.Attempts != 1 {
		t.Errorf("permanent: attempts = %d, want 1", ev.Job.Attempts)
	}
}

func TestThirdFailureMarksFailed(t *testing.T) {
	d := openDB(t)
	cl := newClock(base)
	r := newRunner(d, nil, cl)
	r.Register(runner.Kind{Name: "retry3", Run: func(_ context.Context, _ *runner.Job) error { return errors.New("always fails") }})
	ch, unsub := r.Subscribe()
	defer unsub()
	r.Enqueue(runner.Spec{Kind: "retry3", Subject: "1"}) //nolint:errcheck
	cancel := startRunner(t, r)
	defer cancel()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Job.Kind != "retry3" {
				continue
			}
			if ev.Job.State == "failed" {
				if ev.Job.Attempts != 3 {
					t.Errorf("failed at attempts=%d, want 3", ev.Job.Attempts)
				}
				return
			}
			if ev.Job.State == "queued" {
				cl.Advance(3 * time.Minute)
			}
		case <-time.After(200 * time.Millisecond):
			cl.Advance(3 * time.Minute)
		case <-timeout:
			t.Fatal("job never reached failed after 3 attempts")
		}
	}
}

func TestPanicRecoveredAsError(t *testing.T) {
	d := openDB(t)
	r := newRunner(d, nil, nil)
	r.Register(runner.Kind{Name: "panic", Run: func(_ context.Context, _ *runner.Job) error { panic("test panic") }})
	ch, unsub := r.Subscribe()
	defer unsub()
	r.Enqueue(runner.Spec{Kind: "panic", Subject: "1"}) //nolint:errcheck
	cancel := startRunner(t, r)
	defer cancel()
	waitEvent(t, ch, func(e runner.Event) bool {
		return e.Job.Kind == "panic" && (e.Job.State == "queued" || e.Job.State == "failed")
	})
}

func TestShutdownLeavesJobRunningForRestart(t *testing.T) {
	d := openDB(t)
	started := make(chan struct{})
	r1 := newRunner(d, nil, nil)
	r1.Register(runner.Kind{Name: "slow", Resume: true, Run: func(ctx context.Context, _ *runner.Job) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}})
	id, _ := r1.Enqueue(runner.Spec{Kind: "slow", Subject: "1"})
	ctx1, cancel1 := context.WithCancel(context.Background())
	go r1.Start(ctx1) //nolint:errcheck
	<-started
	cancel1()
	time.Sleep(50 * time.Millisecond)
	var state string
	var attempts int
	d.QueryRow(`SELECT state, attempts FROM job WHERE id=?`, id).Scan(&state, &attempts) //nolint:errcheck
	if state != "running" {
		t.Errorf("after shutdown: state=%q, want running", state)
	}
	if attempts != 1 {
		t.Errorf("after shutdown: attempts=%d, want 1", attempts)
	}
}

// --- restart ---

func TestRestart(t *testing.T) {
	cases := []struct {
		name     string
		resume   bool
		attempts int
		want     string
	}{
		{"resume below limit", true, 1, "queued"},
		{"resume at limit", true, 3, "failed"},
		{"no resume", false, 1, "interrupted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := openDB(t)
			ts := base.UTC().Format(time.RFC3339)
			_, err := d.Exec(
				`INSERT INTO job (kind,subject,priority,state,attempts,not_before,progress,error,params,created,updated)
				 VALUES ('k','x',0,'running',?,?,'{}','',NULL,?,?)`,
				tc.attempts, ts, ts, ts,
			)
			if err != nil {
				t.Fatalf("insert: %v", err)
			}
			r := newRunner(d, nil, nil)
			r.Register(runner.Kind{Name: "k", Resume: tc.resume, Run: func(_ context.Context, _ *runner.Job) error { return nil }})
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			r.Start(ctx) //nolint:errcheck
			var state string
			d.QueryRow(`SELECT state FROM job WHERE kind='k' AND subject='x'`).Scan(&state) //nolint:errcheck
			if state != tc.want {
				t.Errorf("state: got %q, want %q", state, tc.want)
			}
		})
	}
}

// --- clean-up ---

func TestOldFinishedJobsDeletedAtStart(t *testing.T) {
	d := openDB(t)
	old := base.Add(-91 * 24 * time.Hour).UTC().Format(time.RFC3339)
	now := base.UTC().Format(time.RFC3339)
	insert := `INSERT INTO job (kind,subject,priority,state,attempts,not_before,progress,error,params,created,updated) VALUES (?,'x',0,'done',1,?,'{}','',NULL,?,?)`
	d.Exec(insert, "old", now, old, now)    //nolint:errcheck
	d.Exec(insert, "recent", now, now, now) //nolint:errcheck
	r := newRunner(d, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.Start(ctx) //nolint:errcheck
	var n int
	d.QueryRow(`SELECT count(*) FROM job WHERE kind='old'`).Scan(&n) //nolint:errcheck
	if n != 0 {
		t.Errorf("old job not deleted: count=%d", n)
	}
	d.QueryRow(`SELECT count(*) FROM job WHERE kind='recent'`).Scan(&n) //nolint:errcheck
	if n != 1 {
		t.Errorf("recent job deleted: count=%d", n)
	}
}

// --- stable ordering ---

func TestStableOrderWithinOneSec(t *testing.T) {
	d := openDB(t)
	r := newRunner(d, nil, nil)
	r.Register(runner.Kind{Name: "ord", Run: func(_ context.Context, _ *runner.Job) error { return nil }})
	id1, _ := r.Enqueue(runner.Spec{Kind: "ord", Subject: "first"})
	id2, _ := r.Enqueue(runner.Spec{Kind: "ord", Subject: "second"})
	active := r.Active()
	if len(active) != 2 {
		t.Fatalf("want 2 active, got %d", len(active))
	}
	if active[0].ID != id1 || active[1].ID != id2 {
		t.Errorf("Active ids: [%d,%d], want [%d,%d]", active[0].ID, active[1].ID, id1, id2)
	}
}

// --- Last and Active ---

func TestLastAndActive(t *testing.T) {
	d := openDB(t)
	hold := make(chan struct{})
	r := newRunner(d, nil, nil)
	r.Register(runner.Kind{Name: "q", Run: func(ctx context.Context, _ *runner.Job) error { <-hold; return nil }})
	r.Enqueue(runner.Spec{Kind: "q", Subject: "a"}) //nolint:errcheck
	r.Enqueue(runner.Spec{Kind: "q", Subject: "b"}) //nolint:errcheck
	ch, unsub := r.Subscribe()
	defer unsub()
	cancel := startRunner(t, r)
	defer cancel()
	waitEvent(t, ch, func(e runner.Event) bool { return e.Job.State == "running" })
	if len(r.Active()) == 0 {
		t.Error("Active returned nothing while jobs are running/queued")
	}
	close(hold)
	waitEvent(t, ch, func(e runner.Event) bool { return e.Job.State == "done" })
	time.Sleep(30 * time.Millisecond)
	if _, ok := r.Last("q"); !ok {
		t.Error("Last returned false after job completed")
	}
}
