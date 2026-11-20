package inbox_test

import (
	"testing"

	"github.com/sifty-works/sifty-server/internal/industry"
)

func TestGetIndustries_ListsFixedNames(t *testing.T) {
	body := apiGet(t, newTestServer(t, openTestDB(t)), "/api/industries")
	industries, _ := body["industries"].([]any)
	names := industry.Names()
	if len(industries) != len(names) {
		t.Fatalf("got %d industries, want %d", len(industries), len(names))
	}
	first, _ := industries[0].(map[string]any)
	if name, _ := first["name"].(string); name != names[0] {
		t.Errorf("first name: got %q, want %q", name, names[0])
	}
}
