package fit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/sifty-works/sifty-server/internal/apperr"
	"github.com/sifty-works/sifty-server/internal/candidate"
	"github.com/sifty-works/sifty-server/internal/catalog"
	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/listing"
	"github.com/sifty-works/sifty-server/internal/reading"
)

// ScoringVersion rises when Score or the reading by code changes how a result
// is computed, so every stored result becomes out of date.
const ScoringVersion = 1

// current holds when the fit row "f" of a vacancy "v" is current; its one
// argument is the profile's inputs hash.
const current = `(f.inputs IS ? AND f.version IS v.version)`

// Join joins the fit row "f" of a profile to a vacancy "v"; its one argument
// is the profile id.
const Join = `LEFT JOIN fit f ON f.profile=? AND f.vacancy=v.id`

// Columns are the columns of a stored result read through Join (see Stored).
const Columns = `f.state, f.score, f.inputs, f.version IS v.version`

// OrderBestFit puts current numbers first, highest first, then out-of-date
// and unscored results, each by id; its one argument is the inputs hash.
const OrderBestFit = `ORDER BY CASE WHEN ` + current + ` AND f.state IS 'scored' THEN f.score ELSE -1 END DESC, v.id ASC`

// Stored is a stored result as read by Columns; State is empty when there is none.
type Stored struct {
	State              sql.NullString
	Score, Inputs      sql.NullInt64
	SameVacancyVersion bool
}

// Dest returns the scan destinations of Columns.
func (s *Stored) Dest() []any {
	return []any{&s.State, &s.Score, &s.Inputs, &s.SameVacancyVersion}
}

// Current reports whether the result is current for a profile's inputs hash.
func (s Stored) Current(inputs int64) bool {
	return s.Inputs.Valid && s.Inputs.Int64 == inputs && s.SameVacancyVersion
}

// Buckets returns the predicate selecting vacancies whose current result is
// in one of the named buckets: strong, partial, weak, or not_scored for no
// current number. Unknown names are ignored; "" when none is known.
func Buckets(names []string, inputs int64) (string, []any) {
	scored := current + ` AND f.state IS 'scored'`
	var parts []string
	var args []any
	for _, n := range names {
		p, ok := map[string]string{
			"strong":     fmt.Sprintf("%s AND f.score>=%d", scored, StrongFrom),
			"partial":    fmt.Sprintf("%s AND f.score>=%d AND f.score<%d", scored, PartialFrom, StrongFrom),
			"weak":       fmt.Sprintf("%s AND f.score<%d", scored, PartialFrom),
			"not_scored": "NOT (" + scored + ")",
		}[n]
		if ok {
			parts, args = append(parts, "("+p+")"), append(args, inputs)
		}
	}
	if len(parts) == 0 {
		return "", nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

// Band computes Fits you of one vacancy for one profile as the vacancy page shows it.
func Band(ctx context.Context, database *db.DB, profileID, vacancyID int64, r Rules) (Fit, error) {
	p, err := LoadProfile(ctx, database, profileID, r)
	if err != nil {
		return Fit{}, err
	}
	v, ok, err := LoadVacancy(ctx, catalog.New(database, nil), vacancyID)
	if err != nil {
		return Fit{}, err
	}
	if !ok {
		return Fit{}, apperr.NotFound{Message: "Role not found."}
	}
	return Score(p.View, reading.FromCode(v), r), nil
}

// Profile is what Score compares for one profile and the hash of those inputs.
type Profile struct {
	ID     int64
	View   ProfileView
	Inputs int64
}

// Inputs hashes everything a result depends on besides the vacancy: the
// effective goals, the work authorizations, the weights, the rates and the
// scoring version.
func Inputs(p ProfileView, r Rules) int64 {
	b, _ := json.Marshal(struct {
		V int
		P ProfileView
		R Rules
	}{ScoringVersion, p, r})
	h := fnv.New64a()
	h.Write(b) //nolint:errcheck
	return int64(h.Sum64())
}

// LoadProfile returns the effective goals of a profile and My data's work
// authorizations, with their hash.
func LoadProfile(ctx context.Context, database *db.DB, id int64, r Rules) (Profile, error) {
	pd, err := candidate.EffectiveProfile(ctx, database, id)
	if err != nil {
		return Profile{}, err
	}
	me, err := candidate.LoadMe(ctx, database)
	if err != nil {
		return Profile{}, err
	}
	v := ProfileView{Goals: candidate.Goals(pd), Authorizations: me.WorkAuthorizations}
	return Profile{id, v, Inputs(v, r)}, nil
}

// LoadProfiles returns every profile in the order of their ids.
func LoadProfiles(ctx context.Context, database *db.DB, r Rules) ([]Profile, error) {
	rows, err := database.QueryContext(ctx, `SELECT id FROM profile ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("fit: profiles: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("fit: profiles: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("fit: profiles: %w", err)
	}
	var out []Profile
	for _, id := range ids {
		p, err := LoadProfile(ctx, database, id, r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// LoadVacancy returns what code reads a vacancy from; false when it does not exist.
func LoadVacancy(ctx context.Context, cat *catalog.Catalog, id int64) (reading.Vacancy, bool, error) {
	batch, err := cat.VacancyTexts(ctx, id-1, 1)
	if err != nil || len(batch) == 0 || batch[0].ID != id {
		return reading.Vacancy{}, false, err
	}
	v, err := vacancy(batch[0])
	return v, err == nil, err
}

// listingKey hashes the vacancy version and the listing fields code reads
// besides the text.
func listingKey(t catalog.VacancyText) int64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d\x00%s\x00%s\x00%s\x00%s", t.Version, t.Title, t.Location, t.TagsJSON, t.FieldsJSON)
	return int64(h.Sum64())
}

func vacancy(t catalog.VacancyText) (reading.Vacancy, error) {
	v := reading.Vacancy{Title: t.Title, Location: t.Location, Full: listing.TextState(t.TextState) == listing.TextFull}
	json.Unmarshal([]byte(t.TagsJSON), &v.Tags)     //nolint:errcheck
	json.Unmarshal([]byte(t.FieldsJSON), &v.Fields) //nolint:errcheck
	html, err := catalog.Body(t.Body)
	if err != nil {
		return v, fmt.Errorf("fit: vacancy %d: %w", t.ID, err)
	}
	v.Text = listing.PlainText(html)
	return v, nil
}

// Recompute brings the stored results up to date. Each pass selects, per
// profile, the vacancies whose result is missing or out of date and computes
// only those; passes repeat until one writes nothing, so a change made while
// it runs is still applied.
func Recompute(ctx context.Context, database *db.DB, rules func() Rules, progress func(done, total int, current string)) error {
	cat := catalog.New(database, nil)
	for {
		n, err := recomputePass(ctx, database, cat, rules(), progress)
		if err != nil || n == 0 {
			return err
		}
	}
}

func recomputePass(ctx context.Context, database *db.DB, cat *catalog.Catalog, r Rules, progress func(int, int, string)) (int, error) {
	profiles, err := LoadProfiles(ctx, database, r)
	if err != nil {
		return 0, err
	}
	stale, total := map[int64][]Profile{}, 0
	for _, p := range profiles {
		// Missing or profile-outdated results, and results whose listing
		// fields changed without a text change; the bodies are not read.
		rows, err := database.QueryContext(ctx, `SELECT v.id, v.version, v.title, v.location, v.tags, v.source_fields,
			`+current+`, f.listing FROM vacancy v `+Join, p.Inputs, p.ID)
		if err != nil {
			return 0, fmt.Errorf("fit: out of date: %w", err)
		}
		for rows.Next() {
			var t catalog.VacancyText
			var ok bool
			var stored sql.NullInt64
			if err := rows.Scan(&t.ID, &t.Version, &t.Title, &t.Location, &t.TagsJSON, &t.FieldsJSON, &ok, &stored); err != nil {
				rows.Close()
				return 0, fmt.Errorf("fit: out of date: %w", err)
			}
			if !ok || stored.Int64 != listingKey(t) {
				stale[t.ID] = append(stale[t.ID], p)
				total++
			}
		}
		rows.Close()
	}
	written := 0
	for after := int64(0); total > 0; {
		batch, err := cat.VacancyTexts(ctx, after, 200)
		if err != nil || len(batch) == 0 {
			return written, err
		}
		after = batch[len(batch)-1].ID
		n, err := writeBatch(ctx, database, batch, stale, r)
		if err != nil {
			return written, err
		}
		written += n
		progress(written, total, "")
	}
	return written, nil
}

// writeBatch computes and stores the out-of-date results of a batch of
// vacancies in one transaction. A profile or vacancy deleted meanwhile is skipped.
func writeBatch(ctx context.Context, database *db.DB, batch []catalog.VacancyText, stale map[int64][]Profile, r Rules) (int, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("fit: write: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	n := int64(0)
	for _, t := range batch {
		if len(stale[t.ID]) == 0 {
			continue
		}
		v, err := vacancy(t)
		if err != nil {
			return 0, err
		}
		read := reading.FromCode(v)
		for _, p := range stale[t.ID] {
			f := Score(p.View, read, r)
			var score any
			if f.State == Scored {
				score = f.Score
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO fit (profile, vacancy, version, inputs, listing, state, score)
				SELECT ?, ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM profile WHERE id=?) AND EXISTS (SELECT 1 FROM vacancy WHERE id=?)
				ON CONFLICT (profile, vacancy) DO UPDATE SET version=excluded.version, inputs=excluded.inputs,
				listing=excluded.listing, state=excluded.state, score=excluded.score`,
				p.ID, t.ID, t.Version, p.Inputs, listingKey(t), f.State, score, p.ID, t.ID)
			if err != nil {
				return 0, fmt.Errorf("fit: write %d: %w", t.ID, err)
			}
			k, _ := res.RowsAffected()
			n += k
		}
	}
	return int(n), tx.Commit()
}
