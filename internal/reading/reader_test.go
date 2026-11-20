package reading_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sifty-works/sifty-server/internal/models"
	"github.com/sifty-works/sifty-server/internal/reading"
)

func vacancyText() string {
	return "We are hiring a backend engineer.\n" +
		"You must have 5 years of Go experience.\n" +
		"Knowledge of PostgreSQL or MySQL is required.\n" +
		"The role is fully remote.\n" +
		"Compensation: USD 120,000–150,000 per year.\n" +
		"Employment type: full-time."
}

func makeReply(requirements []map[string]any, conditions map[string]any) string {
	out := map[string]any{"requirements": requirements, "conditions": conditions}
	b, _ := json.Marshal(out)
	return string(b)
}

func TestRead_clean_answer_gives_offsets_matching_quotes(t *testing.T) {
	text := vacancyText()
	quote := "You must have 5 years of Go experience."
	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: makeReply(
		[]map[string]any{{"text": "5 years Go", "kind": "experience", "level": "required", "quote": quote, "alternatives": []string{}}},
		map[string]any{},
	)})

	rec, _, err := reading.Read(context.Background(), fm, reading.ReadInput{Text: text})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rec.Requirements) != 1 {
		t.Fatalf("requirements count = %d, want 1", len(rec.Requirements))
	}
	r := rec.Requirements[0]
	if r.Quote.Start < 0 || r.Quote.End <= r.Quote.Start {
		t.Fatalf("quote offsets invalid: start=%d end=%d", r.Quote.Start, r.Quote.End)
	}
	if text[r.Quote.Start:r.Quote.End] != quote {
		t.Errorf("text[%d:%d] = %q, want %q", r.Quote.Start, r.Quote.End, text[r.Quote.Start:r.Quote.End], quote)
	}
}

func TestRead_typographic_quote_found(t *testing.T) {
	text := "You’re required to have experience with Go."
	quote := "You’re required to have experience with Go."
	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: makeReply(
		[]map[string]any{{"text": "Go experience", "kind": "skill", "level": "required", "quote": quote, "alternatives": []string{}}},
		map[string]any{},
	)})

	rec, _, err := reading.Read(context.Background(), fm, reading.ReadInput{Text: text})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rec.Requirements) == 0 {
		t.Fatal("requirement dropped: typographic quote should be found in text")
	}
}

func TestRead_quote_missing_from_text_drops_item(t *testing.T) {
	text := "We need a backend engineer with Python skills."
	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: makeReply(
		[]map[string]any{
			{"text": "Go", "kind": "skill", "level": "required", "quote": "extensive Go expertise required", "alternatives": []string{}},
			{"text": "Python", "kind": "skill", "level": "required", "quote": "We need a backend engineer with Python skills.", "alternatives": []string{}},
		},
		map[string]any{},
	)})

	rec, _, err := reading.Read(context.Background(), fm, reading.ReadInput{Text: text})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rec.Requirements) != 1 {
		t.Fatalf("requirements count = %d, want 1: only the item with a quote in the text should survive", len(rec.Requirements))
	}
	if rec.Requirements[0].Text != "Python" {
		t.Errorf("kept requirement = %q, want Python", rec.Requirements[0].Text)
	}
}

func TestRead_alternatives_not_in_text_dropped(t *testing.T) {
	text := "Experience with a backend language such as Go or Python is required."
	quote := "Experience with a backend language such as Go or Python is required."
	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: makeReply(
		[]map[string]any{{
			"text": "Backend language", "kind": "skill", "level": "required", "quote": quote,
			"alternatives": []string{"Go", "Python", "Rust"},
		}},
		map[string]any{},
	)})

	rec, _, err := reading.Read(context.Background(), fm, reading.ReadInput{Text: text})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rec.Requirements) == 0 {
		t.Fatal("requirement dropped unexpectedly")
	}
	for _, a := range rec.Requirements[0].Alts {
		if a == "Rust" {
			t.Error("alternative 'Rust' not in text should be dropped")
		}
	}
}

func TestRead_single_alternative_means_none(t *testing.T) {
	text := "Experience with Go is required."
	quote := "Experience with Go is required."
	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: makeReply(
		[]map[string]any{{
			"text": "Go", "kind": "skill", "level": "required", "quote": quote,
			"alternatives": []string{"Go"},
		}},
		map[string]any{},
	)})

	rec, _, err := reading.Read(context.Background(), fm, reading.ReadInput{Text: text})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rec.Requirements) == 0 {
		t.Fatal("requirement dropped unexpectedly")
	}
	if len(rec.Requirements[0].Alts) != 0 {
		t.Errorf("alts = %v, want nil: single alternative should become none", rec.Requirements[0].Alts)
	}
}

func TestRead_duplicate_dropped(t *testing.T) {
	text := "You must have Go experience. Go skills are required."
	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: makeReply(
		[]map[string]any{
			{"text": "Go", "kind": "skill", "level": "required", "quote": "You must have Go experience.", "alternatives": []string{}},
			{"text": "Go", "kind": "skill", "level": "required", "quote": "Go skills are required.", "alternatives": []string{}},
		},
		map[string]any{},
	)})

	rec, _, err := reading.Read(context.Background(), fm, reading.ReadInput{Text: text})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rec.Requirements) != 1 {
		t.Errorf("requirements count = %d, want 1: duplicate should be dropped", len(rec.Requirements))
	}
}

func TestRead_work_mode_quote_about_duties_dropped(t *testing.T) {
	text := "This is a 50/50 hybrid role between engineering and product management."
	quote := "This is a 50/50 hybrid role between engineering and product management."
	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: makeReply(
		[]map[string]any{},
		map[string]any{"work_mode": map[string]any{"value": "hybrid", "quote": quote}},
	)})

	rec, _, err := reading.Read(context.Background(), fm, reading.ReadInput{Text: text})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rec.WorkMode != nil {
		t.Errorf("work_mode = %v, want nil: duty-hybrid quote should be dropped", rec.WorkMode)
	}
}

func TestRead_request_names_only_missing_conditions(t *testing.T) {
	text := "We are looking for an engineer. Full-time employment."
	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: makeReply([]map[string]any{}, map[string]any{
		"location": map[string]any{"value": "Berlin", "quote": "We are looking for an engineer."},
	})})

	have := reading.VacancyReading{
		Money:      &reading.Money{Min: 100000},
		WorkModes:  []string{"remote"},
		Engagement: "employment",
		TimeZone:   "UTC+1",
		Hours:      &reading.Hours{Mode: "full_time"},
	}
	reading.Read(context.Background(), fm, reading.ReadInput{Text: text, Have: have})

	calls := fm.Calls()
	if len(calls) == 0 {
		t.Fatal("no model calls made")
	}
	prompt := calls[0].Prompt
	mcIdx := strings.Index(prompt, "\nMISSING CONDITIONS:")
	if mcIdx < 0 {
		t.Fatal("prompt has no MISSING CONDITIONS section")
	}
	schemaIdx := strings.Index(prompt[mcIdx:], "\nSCHEMA")
	if schemaIdx < 0 {
		schemaIdx = len(prompt) - mcIdx
	}
	mcSection := prompt[mcIdx : mcIdx+schemaIdx]
	for _, name := range []string{"money", "work_mode", "engagement", "timezone", "hours"} {
		if strings.Contains(mcSection, name) {
			t.Errorf("MISSING CONDITIONS section contains %q but that condition was already found", name)
		}
	}
	if !strings.Contains(mcSection, "location") {
		t.Error("MISSING CONDITIONS section should contain 'location' as it is missing")
	}
}

func TestRead_empty_answer_on_text_with_requirements_retried(t *testing.T) {
	text := "You have 5 years of backend experience required. Must know Go."
	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: makeReply([]map[string]any{}, map[string]any{})})
	fm.AddReply(models.Response{Text: makeReply(
		[]map[string]any{{"text": "5 years backend", "kind": "experience", "level": "required", "quote": "You have 5 years of backend experience required.", "alternatives": []string{}}},
		map[string]any{},
	)})

	rec, _, err := reading.Read(context.Background(), fm, reading.ReadInput{Text: text})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(fm.Calls()) != 2 {
		t.Errorf("model calls = %d, want 2: should retry once", len(fm.Calls()))
	}
	if len(rec.Requirements) == 0 {
		t.Error("second answer should be used")
	}
}

func TestRead_all_items_fail_retried(t *testing.T) {
	text := "You must have 5 years of Python experience. Django knowledge required."
	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: makeReply(
		[]map[string]any{{"text": "Python", "kind": "skill", "level": "required", "quote": "five years Python expertise", "alternatives": []string{}}},
		map[string]any{},
	)})
	fm.AddReply(models.Response{Text: makeReply(
		[]map[string]any{{"text": "Python", "kind": "skill", "level": "required", "quote": "You must have 5 years of Python experience.", "alternatives": []string{}}},
		map[string]any{},
	)})

	rec, _, err := reading.Read(context.Background(), fm, reading.ReadInput{Text: text})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(fm.Calls()) != 2 {
		t.Errorf("model calls = %d, want 2: all-fail should retry once", len(fm.Calls()))
	}
	if len(rec.Requirements) == 0 {
		t.Error("second answer requirements should be used")
	}
}

func TestRead_reader_version_includes_hash(t *testing.T) {
	v := reading.ReaderVersion()
	if !strings.HasPrefix(v, "reading/1+") {
		t.Errorf("reader version = %q, want prefix reading/1+", v)
	}
	if len(v) < len("reading/1+")+8 {
		t.Errorf("reader version %q too short", v)
	}
}
