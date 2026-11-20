package reading_test

import (
	"testing"

	"github.com/sifty-works/sifty-server/internal/reading"
)

func TestFromCode_engagement_variants(t *testing.T) {
	cases := []struct {
		name       string
		fieldValue string
		want       string
	}{
		{"FullTime camel", "FullTime", "employment"},
		{"full_time underscore", "full_time", "employment"},
		{"Full-Time hyphen", "Full-Time", "employment"},
		{"PartTime camel", "PartTime", "employment"},
		{"part_time underscore", "part_time", "employment"},
		{"CONTRACTOR upper", "CONTRACTOR", "contract"},
		{"Intern", "Intern", "contract"},
		{"Temporary", "Temporary", "contract"},
		{"Freelance", "Freelance", "contract"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := reading.Vacancy{Fields: map[string]string{"employment_type": tc.fieldValue}, Full: true}
			r := reading.FromCode(v)
			if r.Engagement != tc.want {
				t.Errorf("engagement = %q, want %q", r.Engagement, tc.want)
			}
		})
	}
}

func TestFromCode_hours_variants(t *testing.T) {
	cases := []struct {
		name       string
		fieldValue string
		wantMode   string
	}{
		{"FullTime camel", "FullTime", "full_time"},
		{"full_time underscore", "full_time", "full_time"},
		{"PartTime camel", "PartTime", "part_time"},
		{"part_time underscore", "part_time", "part_time"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := reading.Vacancy{Fields: map[string]string{"employment_type": tc.fieldValue}, Full: true}
			r := reading.FromCode(v)
			if r.Hours == nil || r.Hours.Mode != tc.wantMode {
				t.Errorf("hours.mode = %v, want %q", r.Hours, tc.wantMode)
			}
		})
	}
}

func TestFromCode_workplace_onsite_or_remote(t *testing.T) {
	v := reading.Vacancy{Fields: map[string]string{"workplace_type": "ONSITE_OR_REMOTE"}, Full: true}
	r := reading.FromCode(v)
	hasRemote, hasOnsite := false, false
	for _, m := range r.WorkModes {
		if m == "remote" {
			hasRemote = true
		}
		if m == "onsite" {
			hasOnsite = true
		}
	}
	if !hasRemote || !hasOnsite {
		t.Errorf("work modes = %v, want both remote and onsite", r.WorkModes)
	}
}

func TestFromCode_salary_k_suffix_range(t *testing.T) {
	v := reading.Vacancy{Fields: map[string]string{"salary": "$160-230K"}, Full: true}
	r := reading.FromCode(v)
	if r.Money == nil {
		t.Fatal("money is nil")
	}
	if r.Money.Min != 160000 {
		t.Errorf("min = %.f, want 160000", r.Money.Min)
	}
	if r.Money.Max != 230000 {
		t.Errorf("max = %.f, want 230000", r.Money.Max)
	}
}

func TestFromCode_salary_period_variants(t *testing.T) {
	cases := []struct {
		name       string
		salary     string
		wantPeriod string
	}{
		{"per-year-salary", "180000-220000 USD per-year-salary", "year"},
		{"yearly", "USD 100000 yearly", "year"},
		{"bare year", "1 year", "year"},
		{"bare month", "1 month", "month"},
		{"bare hour", "1 hour", "hour"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := reading.Vacancy{Fields: map[string]string{"salary": tc.salary}, Full: true}
			r := reading.FromCode(v)
			if r.Money == nil {
				t.Fatalf("money is nil for %q", tc.salary)
			}
			if r.Money.Period != tc.wantPeriod {
				t.Errorf("period = %q, want %q", r.Money.Period, tc.wantPeriod)
			}
		})
	}
}

func TestFromCode_engagement_intern_not_international(t *testing.T) {
	v := reading.Vacancy{Fields: map[string]string{"employment_type": "International team"}, Full: true}
	r := reading.FromCode(v)
	if r.Engagement != "" {
		t.Errorf("engagement = %q, want empty", r.Engagement)
	}
}

func TestFromCode_salary_hours_per_week_not_hourly(t *testing.T) {
	v := reading.Vacancy{Fields: map[string]string{"salary": "$120k, 40 hours a week"}, Full: true}
	r := reading.FromCode(v)
	if r.Money != nil && r.Money.Period == "hour" {
		t.Errorf("period = %q, want not hour", r.Money.Period)
	}
}

func TestFromCode_salary_both_k(t *testing.T) {
	v := reading.Vacancy{Fields: map[string]string{"salary": "$225K – $300K"}, Full: true}
	r := reading.FromCode(v)
	if r.Money == nil {
		t.Fatal("money is nil")
	}
	if r.Money.Min != 225000 {
		t.Errorf("min = %.f, want 225000", r.Money.Min)
	}
	if r.Money.Max != 300000 {
		t.Errorf("max = %.f, want 300000", r.Money.Max)
	}
}
