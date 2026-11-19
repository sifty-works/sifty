package fit_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/sifty-works/sifty-server/internal/catalog"
	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/fit"
	"github.com/sifty-works/sifty-server/internal/listing"
	"github.com/sifty-works/sifty-server/internal/reading"
)

var ctx = context.Background()

// fixture holds two profiles and two vacancies, one without its full text.
func fixture(t *testing.T) (*db.DB, *catalog.Catalog, []int64) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "fit.db"), db.Migrations)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	d.Exec(`UPDATE profile SET data='{"name":"One","targetRoles":["Backend Engineer"]}' WHERE id=1`) //nolint:errcheck
	d.Exec(`INSERT INTO profile (data) VALUES ('{"name":"Two","targetRoles":["Data Analyst"]}')`)    //nolint:errcheck
	cat := catalog.New(d, func() time.Time { return time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) })
	var ids []int64
	for i, p := range []listing.Posting{
		{URL: "https://jobs.example.org/1", Title: "Backend Engineer", Company: "Acme", Text: "<p>Salary: EUR 90,000 per year. Fully remote.</p>", TextState: listing.TextFull},
		{URL: "https://jobs.example.org/2", Title: "Data Analyst", Company: "Globex", Text: "<p>Short note</p>", TextState: listing.TextSummary},
	} {
		id, _, err := cat.Upsert(ctx, p, "fixture")
		if err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	return d, cat, ids
}

func recompute(t *testing.T, d *db.DB, progress func(int, int, string)) {
	t.Helper()
	if progress == nil {
		progress = func(int, int, string) {}
	}
	if err := fit.Recompute(ctx, d, rules, progress); err != nil {
		t.Fatalf("recompute: %v", err)
	}
}

type row struct {
	version, inputs int64
	state           string
	score           sql.NullInt64
}

func stored(t *testing.T, d *db.DB, profile, vacancy int64) row {
	t.Helper()
	var r row
	if err := d.QueryRow(`SELECT version, inputs, state, score FROM fit WHERE profile=? AND vacancy=?`, profile, vacancy).
		Scan(&r.version, &r.inputs, &r.state, &r.score); err != nil {
		t.Fatalf("fit row %d/%d: %v", profile, vacancy, err)
	}
	return r
}

func TestRecomputeStoresTheResultOfScoreForEveryProfileAndVacancy(t *testing.T) {
	d, cat, ids := fixture(t)
	recompute(t, d, nil)
	for _, pid := range []int64{1, 2} {
		p, err := fit.LoadProfile(ctx, d, pid, rules())
		if err != nil {
			t.Fatal(err)
		}
		for _, vid := range ids {
			v, _, _ := fit.LoadVacancy(ctx, cat, vid)
			want := fit.Score(p.View, reading.FromCode(v), rules())
			got := stored(t, d, pid, vid)
			if got.inputs != p.Inputs || got.version != 1 || got.state != want.State || (want.State == fit.Scored) != got.score.Valid || int(got.score.Int64) != want.Score {
				t.Errorf("profile %d vacancy %d: stored %+v, want %s %d", pid, vid, got, want.State, want.Score)
			}
		}
	}
	if got := stored(t, d, 1, ids[1]); got.state != fit.NoText {
		t.Errorf("vacancy without full text: state %q", got.state)
	}
}

func TestRecomputeOnlyRecomputesResultsThatAreOutOfDate(t *testing.T) {
	// Profile 2 and the text of vacancy 2 change, and a vacancy is added.
	d, cat, ids := fixture(t)
	recompute(t, d, nil)
	d.Exec(`UPDATE fit SET score=7 WHERE profile=1`)                                                                  //nolint:errcheck
	d.Exec(`UPDATE profile SET data='{"name":"Two","targetRoles":["Backend Engineer"]}' WHERE id=2`)                  //nolint:errcheck
	cat.Upsert(ctx, listing.Posting{URL: "https://jobs.example.org/3", Title: "Backend Engineer", Company: "Initech", //nolint:errcheck
		Text: "<p>Remote role</p>", TextState: listing.TextFull}, "fixture")
	cat.Upsert(ctx, listing.Posting{URL: "https://jobs.example.org/2", Title: "Data Analyst", Company: "Globex", //nolint:errcheck
		Text: "<p>A longer note</p>", TextState: listing.TextFull}, "fixture")
	recompute(t, d, nil)
	if got := stored(t, d, 1, ids[1]); got.version != 2 || got.state != fit.Scored {
		t.Errorf("result of the changed vacancy not recomputed: %+v", got)
	}
	if got := stored(t, d, 1, ids[0]); got.score.Int64 != 7 {
		t.Errorf("a current result was recomputed: %+v", got)
	}
	p2, _ := fit.LoadProfile(ctx, d, 2, rules())
	if got := stored(t, d, 2, ids[0]); got.inputs != p2.Inputs {
		t.Errorf("result of the changed profile not recomputed: %+v", got)
	}
	var n int
	d.QueryRow(`SELECT count(*) FROM fit`).Scan(&n) //nolint:errcheck
	if n != 6 {
		t.Errorf("rows after a new vacancy: %d, want 6", n)
	}
}

func TestProfileChangedWhileRecomputingIsApplied(t *testing.T) {
	d, _, ids := fixture(t)
	changed := false
	recompute(t, d, func(int, int, string) {
		if !changed {
			changed = true
			d.Exec(`UPDATE profile SET data='{"name":"One","targetRoles":["Designer"]}' WHERE id=1`) //nolint:errcheck
		}
	})
	p, _ := fit.LoadProfile(ctx, d, 1, rules())
	if got := stored(t, d, 1, ids[0]); got.inputs != p.Inputs {
		t.Errorf("the change made during the run was not applied: %+v", got)
	}
}

func TestListingChangeWithoutTextChangeIsRecomputed(t *testing.T) {
	d, cat, ids := fixture(t)
	recompute(t, d, nil)
	d.Exec(`UPDATE fit SET score=7 WHERE profile=1`)                                                                                            //nolint:errcheck
	cat.Upsert(ctx, listing.Posting{URL: "https://jobs.example.org/1", Title: "Backend Engineer", Company: "Acme", Location: "Berlin, Germany", //nolint:errcheck
		Text: "<p>Salary: EUR 90,000 per year. Fully remote.</p>", TextState: listing.TextFull}, "fixture")
	recompute(t, d, nil)
	if got := stored(t, d, 1, ids[0]); got.version != 1 || got.score.Int64 == 7 {
		t.Errorf("a location change without a text change was not recomputed: %+v", got)
	}
}
