package candidate

import (
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

func exp(title, start, end string, current bool) Experience {
	return Experience{Title: title, Start: start, End: end, Current: current}
}

func TestDeriveYears_overlappingRolesCountOnce(t *testing.T) {
	entries := []Experience{
		exp("Software Engineer", "2019-01", "2021-12", false),
		exp("Backend Developer", "2021-06", "2023-06", false),
	}
	dy := DeriveYears(entries, testNow)
	want := 4
	if dy.Total != want {
		t.Errorf("Total: got %d, want %d", dy.Total, want)
	}
}

func TestDeriveYears_currentRoleCountsToToday(t *testing.T) {
	entries := []Experience{
		exp("Lead Engineer", "2024-11", "", true),
	}
	dy := DeriveYears(entries, testNow)
	if dy.Total < 1 {
		t.Errorf("Total with current role: got %d, want ≥1", dy.Total)
	}
	if dy.Leading < 1 {
		t.Errorf("Leading with current lead role: got %d, want ≥1", dy.Leading)
	}
}

func TestDeriveYears_leadingAndEngineering(t *testing.T) {
	entries := []Experience{
		exp("Engineering Manager", "2018-01", "2021-12", false),
		exp("Software Engineer", "2015-01", "2018-01", false),
	}
	dy := DeriveYears(entries, testNow)
	if dy.Leading == 0 {
		t.Error("Engineering Manager should count as leading")
	}
	if dy.Engineering == 0 {
		t.Error("Software Engineer should count as engineering")
	}
	if dy.Total < dy.Leading {
		t.Errorf("Total %d should be ≥ Leading %d", dy.Total, dy.Leading)
	}
}

func TestDeriveYears_undatedRoleSkipped(t *testing.T) {
	entries := []Experience{
		exp("CTO", "", "", false),
		exp("Engineer", "2020-01", "2022-12", false),
	}
	dy := DeriveYears(entries, testNow)
	if dy.Total != 3 {
		t.Errorf("Total: got %d, want 3 (undated role skipped)", dy.Total)
	}
	if dy.Leading != 0 {
		t.Errorf("Leading: got %d, want 0 (undated leading role skipped)", dy.Leading)
	}
}

func TestDeriveYears_yearOnlyFormat(t *testing.T) {
	entries := []Experience{
		exp("Developer", "2021", "2022", false),
	}
	dy := DeriveYears(entries, testNow)
	if dy.Total != 2 {
		t.Errorf("Total with year-only dates: got %d, want 2", dy.Total)
	}
}
