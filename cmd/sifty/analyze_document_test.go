package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sifty-works/sifty-server/internal/candidate"
	"github.com/sifty-works/sifty-server/internal/collect"
	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/models"
	"github.com/sifty-works/sifty-server/internal/runner"
	"github.com/sifty-works/sifty-server/internal/server"
)

func openAnalyzeTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(fmt.Sprintf("%s/test.db", t.TempDir()), db.Migrations)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func newAnalyzeServer(t *testing.T, d *db.DB, r *runner.Runner) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := server.New(server.Options{
		Version:           "test",
		Port:              9999,
		Logger:            logger,
		BinaryUploadPaths: []string{"/api/documents"},
	}, func(mux *http.ServeMux) {
		registerAllRoutes(mux, logger, nil, d, time.Now, nil, nil, nil, nil, t.TempDir(), collect.New(nil, nil, nil, nil, nil, false, nil, logger), r)
	})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func apiGet(t *testing.T, ts *httptest.Server, path string) (map[string]any, int) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	req.Host = "127.0.0.1:9999"
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out, resp.StatusCode
}

func apiSend(t *testing.T, ts *httptest.Server, method, path string, payload any) (map[string]any, int) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		b, _ := json.Marshal(payload)
		body = strings.NewReader(string(b))
	} else {
		body = strings.NewReader("{}")
	}
	req, _ := http.NewRequest(method, ts.URL+path, body)
	req.Host = "127.0.0.1:9999"
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out, resp.StatusCode
}

func pasteAnalyzeDoc(t *testing.T, ts *httptest.Server, name, text string) (map[string]any, int) {
	t.Helper()
	return apiSend(t, ts, http.MethodPost, "/api/documents", map[string]any{"name": name, "text": text})
}

func fakeProposalReply(facts []map[string]any) string {
	out := map[string]any{"facts": facts}
	b, _ := json.Marshal(out)
	return string(b)
}

func emptyProposalReply() string {
	return fakeProposalReply(nil)
}

func waitForDocState(t *testing.T, ts *httptest.Server, docID float64, wantState string, timeout time.Duration) map[string]any {
	t.Helper()
	idStr := fmt.Sprintf("%.0f", docID)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		body, _ := apiGet(t, ts, "/api/documents")
		docs, _ := body["documents"].([]any)
		for _, d := range docs {
			dm, _ := d.(map[string]any)
			if dm["id"] == docID && dm["state"] == wantState {
				return dm
			}
		}
		_ = idStr
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("document %.0f did not reach state %q within %v", docID, wantState, timeout)
	return nil
}

func registerAnalyzeKind(r *runner.Runner, d *db.DB, m models.Model) {
	r.Register(runner.Kind{
		Name:   "analyze_document",
		Resume: false,
		Run: func(runCtx context.Context, j *runner.Job) error {
			return runAnalyzeDocument(runCtx, j, d, m)
		},
	})
}

type pauseOnCall4 struct {
	*models.FakeModel
	n       atomic.Int32
	done    atomic.Bool
	blocked chan struct{}
}

func (p *pauseOnCall4) Complete(ctx context.Context, r models.Request) (models.Response, error) {
	if !p.done.Load() && p.n.Add(1) == 4 {
		close(p.blocked)
		<-ctx.Done()
		p.done.Store(true)
		return models.Response{}, ctx.Err()
	}
	return p.FakeModel.Complete(ctx, r)
}

func startServeOnline(t *testing.T, dir string) (port int, cancel func()) {
	t.Helper()
	ctx, cancelCtx := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx, []string{"serve", "--data-dir", dir, "--port", "0"}, pw, io.Discard)
		pw.Close()
	}()
	sc := bufio.NewScanner(pr)
	portCh := make(chan int, 1)
	go func() {
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "Listening on http://127.0.0.1:") {
				var p int
				if _, err := fmt.Sscanf(line, "Listening on http://127.0.0.1:%d", &p); err == nil {
					portCh <- p
					return
				}
			}
		}
		portCh <- 0
	}()
	select {
	case p := <-portCh:
		if p == 0 {
			cancelCtx()
			t.Fatal("did not receive listening port")
		}
		port = p
	case <-time.After(10 * time.Second):
		cancelCtx()
		t.Fatal("timeout waiting for server to start")
	}
	cancel = func() { cancelCtx(); <-done; pr.Close() }
	return
}

func TestAnalyzeQueuesJobAndDocumentShowsAnalysing(t *testing.T) {
	d := openAnalyzeTestDB(t)
	r := runner.New(d, nil, time.Now, slog.Default())

	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: emptyProposalReply()})
	registerAnalyzeKind(r, d, fm)

	ts := newAnalyzeServer(t, d, r)

	out, status := pasteAnalyzeDoc(t, ts, "My CV", "Experienced Go developer with five years of backend experience.")
	if status != http.StatusOK {
		t.Fatalf("paste doc: status %d body: %v", status, out)
	}
	docID := out["id"].(float64)

	analyzeOut, status := apiSend(t, ts, http.MethodPost, fmt.Sprintf("/api/documents/%.0f/analyze", docID), nil)
	if status != http.StatusOK {
		t.Fatalf("POST analyze: status %d body: %v", status, analyzeOut)
	}
	if analyzeOut["state"] != "analysing" {
		t.Errorf("after analyze: state = %v, want analysing", analyzeOut["state"])
	}

	var queued int
	d.QueryRow(`SELECT count(*) FROM job WHERE kind='analyze_document' AND state IN ('queued','running')`).Scan(&queued)
	if queued == 0 {
		t.Error("analyze_document job not queued")
	}
}

func TestAnalyzeJobSavesFactsWithQuotes(t *testing.T) {
	d := openAnalyzeTestDB(t)
	r := runner.New(d, nil, time.Now, slog.Default())

	docText := "Proficient in Go and Python. English is my native language. I am seeking a Backend Engineer role."

	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: fakeProposalReply([]map[string]any{
		{"field": "skill", "value": "Go", "quote": "Proficient in Go and Python."},
		{"field": "language", "value": "English (Native)", "quote": "English is my native language."},
		{"field": "targetRole", "value": "Backend Engineer", "quote": "seeking a Backend Engineer role."},
	})})
	registerAnalyzeKind(r, d, fm)

	ts := newAnalyzeServer(t, d, r)

	out, _ := pasteAnalyzeDoc(t, ts, "cv.txt", docText)
	docID := out["id"].(float64)

	apiSend(t, ts, http.MethodPost, fmt.Sprintf("/api/documents/%.0f/analyze", docID), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Start(ctx)

	waitForDocState(t, ts, docID, "analysed", 5*time.Second)

	listOut, status := apiGet(t, ts, "/api/proposals")
	if status != http.StatusOK {
		t.Fatalf("GET /api/proposals: status %d", status)
	}
	proposals, _ := listOut["proposals"].([]any)
	if len(proposals) != 3 {
		t.Fatalf("proposals count = %d, want 3", len(proposals))
	}
	fields := map[string]bool{}
	for _, p := range proposals {
		pm, _ := p.(map[string]any)
		fields[pm["field"].(string)] = true
		if pm["quote"] == nil || pm["quote"] == "" {
			t.Errorf("proposal has empty quote: %v", pm)
		}
	}
	for _, f := range []string{"skill", "language", "targetRole"} {
		if !fields[f] {
			t.Errorf("missing proposal for field %q", f)
		}
	}
}

func TestAnalyzeJobDropsFactWithMismatchedQuote(t *testing.T) {
	d := openAnalyzeTestDB(t)
	r := runner.New(d, nil, time.Now, slog.Default())

	docText := "Expert in Rust and C++. Looking for systems programming work."

	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: fakeProposalReply([]map[string]any{
		{"field": "skill", "value": "Rust", "quote": "Expert in Rust and C++."},
		{"field": "skill", "value": "Java", "quote": "This quote does not appear in the document text at all."},
	})})
	registerAnalyzeKind(r, d, fm)

	ts := newAnalyzeServer(t, d, r)

	out, _ := pasteAnalyzeDoc(t, ts, "skills.txt", docText)
	docID := out["id"].(float64)

	apiSend(t, ts, http.MethodPost, fmt.Sprintf("/api/documents/%.0f/analyze", docID), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Start(ctx)

	waitForDocState(t, ts, docID, "analysed", 5*time.Second)

	listOut, _ := apiGet(t, ts, "/api/proposals")
	proposals, _ := listOut["proposals"].([]any)
	if len(proposals) != 1 {
		t.Fatalf("proposals count = %d, want 1 (mismatched quote dropped)", len(proposals))
	}
	pm := proposals[0].(map[string]any)
	if pm["value"] != "Rust" {
		t.Errorf("kept proposal value = %v, want Rust", pm["value"])
	}
}

func TestAnalyzeJobCapAt100Facts(t *testing.T) {
	d := openAnalyzeTestDB(t)
	r := runner.New(d, nil, time.Now, slog.Default())

	var sb strings.Builder
	var facts []map[string]any
	for i := 0; i < 101; i++ {
		skill := fmt.Sprintf("SkillXYZ%d", i)
		line := fmt.Sprintf("Experience with %s is required. ", skill)
		sb.WriteString(line)
		facts = append(facts, map[string]any{
			"field": "skill",
			"value": skill,
			"quote": strings.TrimSpace(line),
		})
	}
	docText := sb.String()

	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: fakeProposalReply(facts)})
	registerAnalyzeKind(r, d, fm)

	ts := newAnalyzeServer(t, d, r)

	out, _ := pasteAnalyzeDoc(t, ts, "many.txt", docText)
	docID := out["id"].(float64)

	apiSend(t, ts, http.MethodPost, fmt.Sprintf("/api/documents/%.0f/analyze", docID), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Start(ctx)

	waitForDocState(t, ts, docID, "analysed", 5*time.Second)

	listOut, _ := apiGet(t, ts, "/api/proposals")
	proposals, _ := listOut["proposals"].([]any)
	if len(proposals) != 100 {
		t.Fatalf("proposals count = %d, want 100 (cap at 100)", len(proposals))
	}
}

func TestAnalyzeDuplicateFilterCoverage(t *testing.T) {
	cases := []struct {
		name     string
		meJSON   string
		profJSON string
		field    string
		value    string
		quote    string
		docText  string
	}{
		{
			name:   "skill already in me skills",
			meJSON: `{"skills":[{"name":"Go","level":"expert"}],"languages":[],"experience":[]}`,
			field:  "skill", value: "Go", quote: "Experienced Go developer.",
			docText: "Experienced Go developer.",
		},
		{
			name:   "skill in absent skills list",
			meJSON: `{"skills":[],"absentSkills":[{"name":"Rust"}],"languages":[],"experience":[]}`,
			field:  "skill", value: "Rust", quote: "Familiar with Rust too.",
			docText: "Familiar with Rust too.",
		},
		{
			name:   "language already in me languages",
			meJSON: `{"skills":[],"languages":[{"name":"French","level":"native"}],"experience":[]}`,
			field:  "language", value: "French (Native)", quote: "French is my first language.",
			docText: "French is my first language.",
		},
		{
			name:     "target role already in active profile",
			meJSON:   `{"skills":[],"languages":[],"experience":[]}`,
			profJSON: `{"name":"Main","targetRoles":["Backend Engineer"]}`,
			field:    "targetRole", value: "Backend Engineer", quote: "Seeking a Backend Engineer position.",
			docText: "Seeking a Backend Engineer position.",
		},
		{
			name:   "experience company already in me experience",
			meJSON: `{"skills":[],"languages":[],"experience":[{"company":"Acme Corp","role":"Engineer","start":"2020-01","current":true}]}`,
			field:  "experience", value: "Engineer at Acme Corp, 2020-present", quote: "Engineer at Acme Corp, 2020-present.",
			docText: "Engineer at Acme Corp, 2020-present.",
		},
		{
			name:   "same value proposed twice in one document",
			meJSON: `{"skills":[],"languages":[],"experience":[]}`,
			field:  "skill", value: "Docker",
			quote:   "Proficient in Docker.",
			docText: "Proficient in Docker. Also Docker certified.",
		},
		{
			name:   "value with internal extra space still matched",
			meJSON: `{"skills":[{"name":"Machine Learning","level":"proficient"}],"languages":[],"experience":[]}`,
			field:  "skill", value: "Machine  Learning", quote: "Machine  Learning expert.",
			docText: "Machine  Learning expert.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := openAnalyzeTestDB(t)

			d.Exec(`INSERT OR REPLACE INTO me (id, data) VALUES (1, ?)`, tc.meJSON)

			if tc.profJSON != "" {
				d.Exec(`INSERT INTO profile (data) VALUES (?)`, tc.profJSON)
				var profID int64
				d.QueryRow(`SELECT last_insert_rowid()`).Scan(&profID)
				d.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('active_profile', ?)`,
					fmt.Sprintf("%d", profID))
			}

			r := runner.New(d, nil, time.Now, slog.Default())

			fm := &models.FakeModel{}
			facts := []map[string]any{
				{"field": tc.field, "value": tc.value, "quote": tc.quote},
			}
			if tc.name == "same value proposed twice in one document" {
				facts = append(facts, map[string]any{
					"field": tc.field, "value": tc.value, "quote": "Also Docker certified.",
				})
			}
			fm.AddReply(models.Response{Text: fakeProposalReply(facts)})
			registerAnalyzeKind(r, d, fm)

			ts := newAnalyzeServer(t, d, r)

			out, _ := pasteAnalyzeDoc(t, ts, "cv.txt", tc.docText)
			docID := out["id"].(float64)

			apiSend(t, ts, http.MethodPost, fmt.Sprintf("/api/documents/%.0f/analyze", docID), nil)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go r.Start(ctx)

			waitForDocState(t, ts, docID, "analysed", 5*time.Second)

			listOut, _ := apiGet(t, ts, "/api/proposals")
			proposals, _ := listOut["proposals"].([]any)
			want := 0
			if tc.name == "same value proposed twice in one document" {
				want = 1
			}
			if len(proposals) != want {
				t.Errorf("proposals = %d, want %d", len(proposals), want)
			}
		})
	}
}

func TestAnalyzeNoProviderSetsOpenSettingsError(t *testing.T) {
	d := openAnalyzeTestDB(t)
	r := runner.New(d, nil, time.Now, slog.Default())

	fm := &models.FakeModel{}
	fm.AddError(fmt.Errorf("%w for reading", models.ErrNoProvider))
	registerAnalyzeKind(r, d, fm)

	ts := newAnalyzeServer(t, d, r)

	out, _ := pasteAnalyzeDoc(t, ts, "cv.txt", "Some document text with real content here.")
	docID := out["id"].(float64)

	apiSend(t, ts, http.MethodPost, fmt.Sprintf("/api/documents/%.0f/analyze", docID), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Start(ctx)

	waitForDocState(t, ts, docID, "error", 5*time.Second)

	body, _ := apiGet(t, ts, "/api/documents")
	docs, _ := body["documents"].([]any)
	if len(docs) == 0 {
		t.Fatal("no documents")
	}
	doc := docs[0].(map[string]any)
	if doc["stateReason"] != "No model is chosen for reading. Open Settings." {
		t.Errorf("stateReason = %v, want no-provider message", doc["stateReason"])
	}
}

func TestAnalyzeSecondRunReplacesToReviewProposals(t *testing.T) {
	d := openAnalyzeTestDB(t)
	r := runner.New(d, nil, time.Now, slog.Default())

	docText := "Experienced in Go. Familiar with Python. Seeking a Backend role."

	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: fakeProposalReply([]map[string]any{
		{"field": "skill", "value": "Go", "quote": "Experienced in Go."},
	})})
	fm.AddReply(models.Response{Text: fakeProposalReply([]map[string]any{
		{"field": "skill", "value": "Python", "quote": "Familiar with Python."},
	})})
	registerAnalyzeKind(r, d, fm)

	ts := newAnalyzeServer(t, d, r)

	out, _ := pasteAnalyzeDoc(t, ts, "cv.txt", docText)
	docID := out["id"].(float64)

	apiSend(t, ts, http.MethodPost, fmt.Sprintf("/api/documents/%.0f/analyze", docID), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Start(ctx)

	waitForDocState(t, ts, docID, "analysed", 5*time.Second)

	listOut, _ := apiGet(t, ts, "/api/proposals")
	proposals, _ := listOut["proposals"].([]any)
	if len(proposals) != 1 || proposals[0].(map[string]any)["value"] != "Go" {
		t.Fatalf("first run: proposals = %v, want [Go]", proposals)
	}

	apiSend(t, ts, http.MethodPost, fmt.Sprintf("/api/documents/%.0f/analyze", docID), nil)

	for i := 0; i < 200; i++ {
		time.Sleep(5 * time.Millisecond)
		listOut, _ = apiGet(t, ts, "/api/proposals")
		proposals, _ = listOut["proposals"].([]any)
		if len(proposals) == 1 {
			if p, ok := proposals[0].(map[string]any); ok && p["value"] == "Python" {
				return
			}
		}
	}
	t.Fatalf("second run did not replace proposals; got: %v", proposals)
}

func TestDeleteDocumentRemovesToReviewProposals(t *testing.T) {
	d := openAnalyzeTestDB(t)
	r := runner.New(d, nil, time.Now, slog.Default())

	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: fakeProposalReply([]map[string]any{
		{"field": "skill", "value": "Rust", "quote": "Expert in Rust."},
	})})
	registerAnalyzeKind(r, d, fm)

	ts := newAnalyzeServer(t, d, r)

	out, _ := pasteAnalyzeDoc(t, ts, "cv.txt", "Expert in Rust. Working on systems software.")
	docID := out["id"].(float64)

	apiSend(t, ts, http.MethodPost, fmt.Sprintf("/api/documents/%.0f/analyze", docID), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Start(ctx)

	waitForDocState(t, ts, docID, "analysed", 5*time.Second)

	listOut, _ := apiGet(t, ts, "/api/proposals")
	proposals, _ := listOut["proposals"].([]any)
	if len(proposals) != 1 {
		t.Fatalf("before delete: %d proposals, want 1", len(proposals))
	}

	idStr := fmt.Sprintf("%.0f", docID)
	_, status := apiSend(t, ts, http.MethodDelete, "/api/documents/"+idStr, nil)
	if status != http.StatusNoContent {
		t.Fatalf("DELETE document: status %d", status)
	}

	listOut, _ = apiGet(t, ts, "/api/proposals")
	proposals, _ = listOut["proposals"].([]any)
	if len(proposals) != 0 {
		t.Errorf("after delete: %d proposals remain, want 0", len(proposals))
	}
}

func TestAnalyzeOneModelCallPerChunk(t *testing.T) {
	d := openAnalyzeTestDB(t)

	var longText strings.Builder
	for longText.Len() < 20000 {
		longText.WriteString("This is a paragraph about software engineering skills. ")
	}
	text := longText.String()

	chunks := candidate.SplitChunks(text)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks for 20KB text, got %d", len(chunks))
	}

	fm := &models.FakeModel{}
	for range chunks {
		fm.AddReply(models.Response{Text: emptyProposalReply()})
	}

	r := runner.New(d, nil, time.Now, slog.Default())
	registerAnalyzeKind(r, d, fm)

	ts := newAnalyzeServer(t, d, r)

	out, _ := pasteAnalyzeDoc(t, ts, "long.txt", text)
	docID := out["id"].(float64)

	apiSend(t, ts, http.MethodPost, fmt.Sprintf("/api/documents/%.0f/analyze", docID), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Start(ctx)

	waitForDocState(t, ts, docID, "analysed", 10*time.Second)

	calls := fm.Calls()
	if len(calls) != len(chunks) {
		t.Errorf("model calls = %d, want %d (one per chunk)", len(calls), len(chunks))
	}
}

func TestAnalyzeDocumentWithNoTextRefused(t *testing.T) {
	d := openAnalyzeTestDB(t)
	ts := newAnalyzeServer(t, d, nil)

	d.Exec(`INSERT INTO document (name, kind, state, state_reason, char_count, evidence, text, file_name, created_at) VALUES ('empty.pdf', 'pdf', 'unavailable', 'PDF text extraction is not available yet.', 0, 0, '', '', datetime('now'))`)
	var docID int64
	d.QueryRow(`SELECT last_insert_rowid()`).Scan(&docID)

	out, status := apiSend(t, ts, http.MethodPost, fmt.Sprintf("/api/documents/%d/analyze", docID), nil)
	if status != http.StatusUnprocessableEntity {
		t.Errorf("analyze unavailable: status %d, want 422; body: %v", status, out)
	}
}

func TestProposalListShowsDocumentName(t *testing.T) {
	d := openAnalyzeTestDB(t)
	r := runner.New(d, nil, time.Now, slog.Default())

	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: fakeProposalReply([]map[string]any{
		{"field": "skill", "value": "Go", "quote": "Expert Go developer."},
	})})
	registerAnalyzeKind(r, d, fm)

	ts := newAnalyzeServer(t, d, r)

	out, _ := pasteAnalyzeDoc(t, ts, "My Resume.txt", "Expert Go developer. Five years experience.")
	docID := out["id"].(float64)

	apiSend(t, ts, http.MethodPost, fmt.Sprintf("/api/documents/%.0f/analyze", docID), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Start(ctx)

	waitForDocState(t, ts, docID, "analysed", 5*time.Second)

	listOut, _ := apiGet(t, ts, "/api/proposals")
	proposals, _ := listOut["proposals"].([]any)
	if len(proposals) == 0 {
		t.Fatal("no proposals")
	}
	pm := proposals[0].(map[string]any)
	if pm["documentName"] != "My Resume.txt" {
		t.Errorf("documentName = %v, want My Resume.txt", pm["documentName"])
	}
}

func TestAnalyzeDelayedErrorKeepsDocumentAnalysing(t *testing.T) {
	retryAt := time.Now().Add(10 * time.Minute)
	cases := []struct {
		name string
		err  error
	}{
		{"WaitError", models.WaitError{RetryAfter: retryAt}},
		{"ErrBulkOnPaid", models.ErrBulkOnPaid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := openAnalyzeTestDB(t)
			r := runner.New(d, nil, time.Now, slog.Default())

			fm := &models.FakeModel{}
			fm.AddError(tc.err)
			registerAnalyzeKind(r, d, fm)

			ts := newAnalyzeServer(t, d, r)

			out, _ := pasteAnalyzeDoc(t, ts, "cv.txt", "Expert Go developer seeking backend roles.")
			docID := out["id"].(float64)

			apiSend(t, ts, http.MethodPost, fmt.Sprintf("/api/documents/%.0f/analyze", docID), nil)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go r.Start(ctx)

			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if j, ok := r.Last("analyze_document"); ok && j.State == "queued" && j.NotBefore.After(time.Now()) {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}

			body, _ := apiGet(t, ts, "/api/documents")
			docs, _ := body["documents"].([]any)
			if len(docs) == 0 {
				t.Fatal("no documents")
			}
			doc := docs[0].(map[string]any)
			if doc["state"] != "analysing" {
				t.Errorf("state after %s = %v, want analysing", tc.name, doc["state"])
			}
		})
	}
}

func TestAnalyzeResumesFromCheckpoint(t *testing.T) {
	var sb strings.Builder
	for sb.Len() < 20000 {
		sb.WriteString("Experienced software engineer with strong distributed systems background. ")
	}
	text := sb.String()

	chunks := candidate.SplitChunks(text)
	if len(chunks) < 3 {
		t.Fatalf("need at least 3 chunks, got %d", len(chunks))
	}

	cases := []struct {
		name       string
		addChunk2  func(*models.FakeModel)
		makeRunner func(*db.DB) (*runner.Runner, func())
		waitState  string
	}{
		{
			name: "WaitError at chunk two",
			addChunk2: func(fm *models.FakeModel) {
				fm.AddError(models.WaitError{RetryAfter: time.Now().Add(-time.Second)})
			},
			makeRunner: func(d *db.DB) (*runner.Runner, func()) {
				return runner.New(d, nil, time.Now, slog.Default()), func() {}
			},
		},
		{
			name: "invalid JSON at chunk two",
			addChunk2: func(fm *models.FakeModel) {
				fm.AddReply(models.Response{Text: "not valid json"})
			},
			makeRunner: func(d *db.DB) (*runner.Runner, func()) {
				var advance atomic.Bool
				r := runner.New(d, nil, func() time.Time {
					if advance.Load() {
						return time.Now().Add(10 * time.Minute)
					}
					return time.Now()
				}, slog.Default())
				return r, func() { advance.Store(true) }
			},
			waitState: "error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := openAnalyzeTestDB(t)

			fm := &models.FakeModel{}
			for i := range chunks {
				if i == 2 {
					tc.addChunk2(fm)
				}
				if i == 1 {
					fm.AddReply(models.Response{Text: fakeProposalReply([]map[string]any{
						{"field": "skill", "value": "Kubernetes", "quote": "distributed systems"},
					})})
				} else {
					fm.AddReply(models.Response{Text: emptyProposalReply()})
				}
			}

			r, trigger := tc.makeRunner(d)
			registerAnalyzeKind(r, d, fm)
			ts := newAnalyzeServer(t, d, r)

			out, _ := pasteAnalyzeDoc(t, ts, "cv.txt", text)
			docID := out["id"].(float64)

			apiSend(t, ts, http.MethodPost, fmt.Sprintf("/api/documents/%.0f/analyze", docID), nil)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go r.Start(ctx)

			if tc.waitState != "" {
				waitForDocState(t, ts, docID, tc.waitState, 5*time.Second)
			}
			trigger()
			waitForDocState(t, ts, docID, "analysed", 10*time.Second)

			calls := fm.Calls()
			if len(calls) != len(chunks)+1 {
				t.Errorf("model calls = %d, want %d (one per chunk plus one interrupted at chunk 2)",
					len(calls), len(chunks)+1)
			}

			listOut, _ := apiGet(t, ts, "/api/proposals")
			proposals, _ := listOut["proposals"].([]any)
			found := false
			for _, p := range proposals {
				pm, _ := p.(map[string]any)
				if pm["value"] == "Kubernetes" {
					found = true
				}
			}
			if !found {
				t.Errorf("proposal for Kubernetes (from chunk 1) not in proposals: %v", proposals)
			}
		})
	}
}

func TestAnalyzeCheckpointSurvivesCancel(t *testing.T) {
	d := openAnalyzeTestDB(t)

	var sb strings.Builder
	for sb.Len() < 40000 {
		sb.WriteString("Experienced software engineer with strong distributed systems background. ")
	}
	text := sb.String()

	chunks := candidate.SplitChunks(text)
	if len(chunks) < 4 {
		t.Fatalf("need at least 4 chunks, got %d", len(chunks))
	}

	fm := &models.FakeModel{}
	for range chunks {
		fm.AddReply(models.Response{Text: emptyProposalReply()})
	}
	for range chunks {
		fm.AddReply(models.Response{Text: emptyProposalReply()})
	}
	m := &pauseOnCall4{FakeModel: fm, blocked: make(chan struct{})}

	r1 := runner.New(d, nil, time.Now, slog.Default())
	registerAnalyzeKind(r1, d, m)
	ts1 := newAnalyzeServer(t, d, r1)

	out, _ := pasteAnalyzeDoc(t, ts1, "cv.txt", text)
	docID := out["id"].(float64)
	apiSend(t, ts1, http.MethodPost, fmt.Sprintf("/api/documents/%.0f/analyze", docID), nil)

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	go r1.Start(ctx1)

	select {
	case <-m.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("job did not reach chunk 3 before timeout")
	}
	cancel1()

	r2 := runner.New(d, nil, time.Now, slog.Default())
	registerAnalyzeKind(r2, d, m)
	ts2 := newAnalyzeServer(t, d, r2)

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go r2.Start(ctx2)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		d.QueryRow(`SELECT state FROM job WHERE kind='analyze_document'`).Scan(&state)
		if state == "interrupted" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	apiSend(t, ts2, http.MethodPost, fmt.Sprintf("/api/documents/%.0f/analyze", docID), nil)

	waitForDocState(t, ts2, docID, "analysed", 10*time.Second)

	calls := fm.Calls()
	if len(calls) != len(chunks) {
		t.Errorf("model calls = %d, want %d (one per chunk despite cancel after chunk 2)",
			len(calls), len(chunks))
	}
}

func TestAnalyzeInterruptedDoesNotAutoRestart(t *testing.T) {
	d := openAnalyzeTestDB(t)

	d.Exec(`INSERT INTO document (name, kind, state, state_reason, char_count, evidence, text, file_name, created_at)
		VALUES ('cv.txt', 'text', 'analysing', '', 100, 0, 'Expert Go engineer.', '', datetime('now'))`)
	var docID int64
	d.QueryRow(`SELECT last_insert_rowid()`).Scan(&docID)

	now := time.Now().UTC().Format(time.RFC3339)
	d.Exec(`INSERT INTO job (kind, subject, priority, state, attempts, not_before, progress, error, params, created, updated)
		VALUES ('analyze_document', ?, 0, 'running', 1, ?, '{}', '', '', ?, ?)`,
		fmt.Sprintf("%d", docID), now, now, now)

	fm := &models.FakeModel{}
	fm.AddReply(models.Response{Text: emptyProposalReply()})

	r := runner.New(d, nil, time.Now, slog.Default())
	registerAnalyzeKind(r, d, fm)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Start(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		d.QueryRow(`SELECT state FROM job WHERE kind='analyze_document'`).Scan(&state)
		if state == "interrupted" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if calls := fm.Calls(); len(calls) != 0 {
		t.Errorf("model calls = %d, want 0 (interrupted job must not auto-restart)", len(calls))
	}
}

func TestAdoptInterruptedDoesNotTakeOtherKind(t *testing.T) {
	d := openAnalyzeTestDB(t)
	r := runner.New(d, nil, time.Now, slog.Default())
	fm := &models.FakeModel{}
	registerAnalyzeKind(r, d, fm)
	ts := newAnalyzeServer(t, d, r)

	out, _ := pasteAnalyzeDoc(t, ts, "cv.txt", "Expert Go engineer with five years of experience.")
	docID := out["id"].(float64)
	subject := fmt.Sprintf("%.0f", docID)

	now := time.Now().UTC().Format(time.RFC3339)
	d.Exec(`INSERT INTO job (kind, subject, priority, state, attempts, not_before, progress, error, params, created, updated)
		VALUES ('other_kind', ?, 0, 'interrupted', 1, ?, '{}', '', '', ?, ?)`,
		subject, now, now, now)
	var otherID int64
	d.QueryRow(`SELECT last_insert_rowid()`).Scan(&otherID)

	apiSend(t, ts, http.MethodPost, fmt.Sprintf("/api/documents/%.0f/analyze", docID), nil)

	var otherState string
	d.QueryRow(`SELECT state FROM job WHERE id=?`, otherID).Scan(&otherState)
	if otherState != "interrupted" {
		t.Errorf("other-kind job state = %q, want interrupted", otherState)
	}

	var analyzeCount int
	d.QueryRow(`SELECT COUNT(*) FROM job WHERE kind='analyze_document' AND state='queued'`).Scan(&analyzeCount)
	if analyzeCount != 1 {
		t.Errorf("queued analyze_document jobs = %d, want 1", analyzeCount)
	}
}

func TestAdoptInterruptedTakesOnlyNewest(t *testing.T) {
	d := openAnalyzeTestDB(t)
	r := runner.New(d, nil, time.Now, slog.Default())
	fm := &models.FakeModel{}
	registerAnalyzeKind(r, d, fm)
	ts := newAnalyzeServer(t, d, r)

	out, _ := pasteAnalyzeDoc(t, ts, "cv.txt", "Expert Go engineer with five years of experience.")
	docID := out["id"].(float64)
	subject := fmt.Sprintf("%.0f", docID)

	now := time.Now().UTC().Format(time.RFC3339)
	d.Exec(`INSERT INTO job (kind, subject, priority, state, attempts, not_before, progress, error, params, created, updated)
		VALUES ('analyze_document', ?, 0, 'interrupted', 1, ?, '{}', '', 'old', ?, ?)`,
		subject, now, now, now)
	var oldID int64
	d.QueryRow(`SELECT last_insert_rowid()`).Scan(&oldID)

	d.Exec(`INSERT INTO job (kind, subject, priority, state, attempts, not_before, progress, error, params, created, updated)
		VALUES ('analyze_document', ?, 0, 'interrupted', 1, ?, '{}', '', 'new', ?, ?)`,
		subject, now, now, now)
	var newID int64
	d.QueryRow(`SELECT last_insert_rowid()`).Scan(&newID)

	apiSend(t, ts, http.MethodPost, fmt.Sprintf("/api/documents/%.0f/analyze", docID), nil)

	var oldState string
	d.QueryRow(`SELECT state FROM job WHERE id=?`, oldID).Scan(&oldState)
	if oldState != "interrupted" {
		t.Errorf("old job state = %q, want interrupted (only newest should be adopted)", oldState)
	}

	var newState string
	d.QueryRow(`SELECT state FROM job WHERE id=?`, newID).Scan(&newState)
	if newState != "queued" {
		t.Errorf("new job state = %q, want queued", newState)
	}

	var count int
	d.QueryRow(`SELECT COUNT(*) FROM job WHERE kind='analyze_document' AND state='queued'`).Scan(&count)
	if count != 1 {
		t.Errorf("queued analyze_document jobs = %d, want 1", count)
	}
}

func TestAnalyzeGatePaidBulkBlocked(t *testing.T) {
	var reqCount atomic.Int64
	fakeProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(fakeProv.Close)

	dir := t.TempDir()
	cfg := fmt.Sprintf(`providers:
  paidbox: {type: openai, model: gpt-turbo, cost: paid, endpoint: %q}
work: {reading: paidbox, shortlist_check: paidbox, preparation: paidbox}
`, fakeProv.URL)
	if err := os.WriteFile(filepath.Join(dir, "sifty.yaml"), []byte(cfg), 0600); err != nil {
		t.Fatalf("write sifty.yaml: %v", err)
	}

	port, cancel := startServeOnline(t, dir)
	defer cancel()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	status, respBody := sendJSON(t, http.MethodPost, base+"/api/documents",
		`{"name":"cv.txt","text":"Experienced engineer with Go and Python skills."}`)
	if status != http.StatusOK {
		t.Fatalf("upload doc: status %d body %s", status, respBody)
	}
	var docOut map[string]any
	json.Unmarshal([]byte(respBody), &docOut)
	docID := docOut["id"].(float64)

	status, _ = sendJSON(t, http.MethodPost, fmt.Sprintf("%s/api/documents/%.0f/analyze", base, docID), `{}`)
	if status != http.StatusOK {
		t.Fatalf("POST analyze: status %d", status)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if reqCount.Load() > 0 {
			t.Errorf("paid provider received %d requests, want 0", reqCount.Load())
			return
		}
		_, body := sendJSON(t, http.MethodGet, base+"/api/documents", ``)
		var out map[string]any
		json.Unmarshal([]byte(body), &out)
		docs, _ := out["documents"].([]any)
		for _, d := range docs {
			dm, _ := d.(map[string]any)
			if dm["id"] != docID {
				continue
			}
			if s, _ := dm["state"].(string); s != "" && s != "analysing" {
				t.Errorf("document state = %v after ErrBulkOnPaid, want analysing", s)
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if reqCount.Load() != 0 {
		t.Errorf("paid provider received %d requests, want 0", reqCount.Load())
	}
}
