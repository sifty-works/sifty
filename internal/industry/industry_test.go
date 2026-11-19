package industry_test

import (
	"path/filepath"
	"testing"

	"github.com/sifty-works/sifty-server/internal/industry"
)

// testVocab returns a small vocabulary for use in tests.
func testVocab() industry.Vocab {
	return industry.Vocab{
		Industries: []industry.Industry{
			{Name: "Blockchain", Keywords: []string{"solidity", "smart contract", "ethereum"}},
			{Name: "Genomics", Keywords: []string{"bioinformatics", "genomic sequencing"}},
			{Name: "Rocketry", Keywords: []string{"rocket propulsion", "orbital mechanics"}},
		},
	}
}

// --- Assign ---

func TestAssign_MatchesKeywordInTitle(t *testing.T) {
	v := testVocab()
	got := industry.Assign("Solidity Developer", "", "", v)
	if len(got) != 1 || got[0] != "Blockchain" {
		t.Errorf("title match: got %v, want [Blockchain]", got)
	}
}

func TestAssign_MatchesKeywordInTags(t *testing.T) {
	v := testVocab()
	got := industry.Assign("Engineer", "ethereum smart-contract", "", v)
	if len(got) != 1 || got[0] != "Blockchain" {
		t.Errorf("tags match: got %v, want [Blockchain]", got)
	}
}

func TestAssign_MatchesKeywordInText(t *testing.T) {
	v := testVocab()
	text := "We build systems that do genomic sequencing of large populations."
	got := industry.Assign("Researcher", "", text, v)
	if len(got) != 1 || got[0] != "Genomics" {
		t.Errorf("text match: got %v, want [Genomics]", got)
	}
}

func TestAssign_MatchesPhraseKeyword(t *testing.T) {
	v := testVocab()
	got := industry.Assign("", "", "experience with smart contract auditing", v)
	if len(got) != 1 || got[0] != "Blockchain" {
		t.Errorf("phrase match: got %v, want [Blockchain]", got)
	}
}

func TestAssign_NoMatchWhenKeywordIsSubstring(t *testing.T) {
	// "art" is part of "start" and "artist" but should not match "art" as a keyword.
	v := industry.Vocab{
		Industries: []industry.Industry{
			{Name: "Arts", Keywords: []string{"artwork studio", "fine arts"}},
		},
	}
	got := industry.Assign("restart artist contract", "startups", "smartwork", v)
	if len(got) != 0 {
		t.Errorf("substring should not match: got %v, want []", got)
	}
}

func TestAssign_WholeWordOnly(t *testing.T) {
	// "golang" in "golang" matches; "golang" should NOT match "longgolang".
	v := industry.Vocab{
		Industries: []industry.Industry{
			{Name: "GoLang", Keywords: []string{"golang"}},
		},
	}
	// Should match.
	got := industry.Assign("golang developer", "", "", v)
	if len(got) != 1 || got[0] != "GoLang" {
		t.Errorf("whole word: got %v, want [GoLang]", got)
	}
	// Should not match when keyword is embedded inside another word.
	got2 := industry.Assign("longgolang developer", "", "", v)
	if len(got2) != 0 {
		t.Errorf("embedded: got %v, want []", got2)
	}
}

func TestAssign_SeveralIndustries(t *testing.T) {
	v := testVocab()
	got := industry.Assign("Ethereum Rocket Engineer", "bioinformatics", "", v)
	// Should match Blockchain (ethereum in title) and Genomics (bioinformatics in tags).
	if len(got) != 2 {
		t.Fatalf("several: got %v, want 2 industries", got)
	}
	names := map[string]bool{got[0]: true, got[1]: true}
	if !names["Blockchain"] || !names["Genomics"] {
		t.Errorf("several: got %v, want Blockchain and Genomics", got)
	}
}

func TestAssign_NoMatchReturnsNil(t *testing.T) {
	v := testVocab()
	got := industry.Assign("General Contractor", "building", "office work", v)
	if got != nil {
		t.Errorf("no match: got %v, want nil", got)
	}
}

func TestAssign_CaseInsensitive(t *testing.T) {
	v := testVocab()
	got := industry.Assign("SOLIDITY ENGINEER", "", "", v)
	if len(got) != 1 || got[0] != "Blockchain" {
		t.Errorf("case insensitive: got %v, want [Blockchain]", got)
	}
}

func TestAssign_EmptyVocab(t *testing.T) {
	v := industry.Vocab{}
	got := industry.Assign("solidity developer", "ethereum", "smart contract work", v)
	if got != nil {
		t.Errorf("empty vocab: got %v, want nil", got)
	}
}

func TestAssign_VacancyWithoutTextKeepsAssignmentFromTitleTags(t *testing.T) {
	// A vacancy without body text should still match on title and tags.
	v := testVocab()
	got := industry.Assign("Ethereum Backend Engineer", "solidity", "", v)
	if len(got) != 1 || got[0] != "Blockchain" {
		t.Errorf("no text: got %v, want [Blockchain]", got)
	}
}

// --- Fixture-based coverage ---

// fixtureVacancy is one invented vacancy used for the coverage test.
type fixtureVacancy struct {
	title string
	text  string
	want  string // expected industry name, or "" when none expected
}

// fixtureVacancies is a representative set of invented tech-job-board postings.
// Titles and texts are purely invented; they carry no real data.
var fixtureVacancies = []fixtureVacancy{
	// Web3
	{"Senior Solidity Engineer", "Build and audit smart contracts on Ethereum. Experience with Solidity required.", "Web3"},
	{"Blockchain Protocol Developer", "Design consensus mechanisms for a layer 2 rollup network.", "Web3"},
	{"Web3 Frontend Developer", "Build dApps for a DeFi platform. Familiar with tokenomics and staking.", "Web3"},
	{"Crypto Infrastructure Engineer", "Maintain wallet infrastructure and on-chain indexers for a stablecoin protocol.", "Web3"},
	{"Zero-Knowledge Proof Engineer", "Implement zk-rollup circuits. Deep knowledge of zero-knowledge cryptography.", "Web3"},
	// Software
	{"Software Engineer", "Build scalable APIs for a SaaS platform. Open source contributions welcome.", "Software"},
	{"Backend Developer", "Python developer to work on microservices infrastructure.", "Software"},
	{"DevOps Engineer", "Manage cloud infrastructure, CI/CD pipelines and developer tools.", "Software"},
	{"Full-Stack Developer", "Build React and Go services for a B2B software product.", "Software"},
	// Security
	{"Security Engineer", "Red team and incident response for a cloud security product.", "Security"},
	{"Penetration Tester", "Conduct pentest engagements; participate in bug bounty programme.", "Security"},
	{"Application Security Engineer", "Appsec and devsecops for a fintech platform.", "Security"},
	// Data and AI
	{"Machine Learning Engineer", "Train large language models using PyTorch and TensorFlow on big data pipelines.", "Data and AI"},
	{"Data Scientist", "Build recommendation systems using deep learning; MLOps experience valued.", "Data and AI"},
	{"AI Research Engineer", "Fine-tuning foundation models; work on a vector database for retrieval.", "Data and AI"},
	// Finance
	{"Quantitative Developer", "Build algorithmic trading systems for a capital markets firm.", "Finance"},
	{"Payments Infrastructure Engineer", "Design payment gateway and open banking integrations.", "Finance"},
	{"Fintech Platform Engineer", "Credit risk analytics for a digital banking neobank.", "Finance"},
	// Healthcare
	{"Healthcare Software Engineer", "Electronic health records platform for a medtech company.", "Healthcare"},
	{"Biotech Data Engineer", "Genomics data pipelines for clinical research and drug discovery.", "Healthcare"},
	// Education
	{"EdTech Engineer", "Learning management system and e-learning platform for a tutoring marketplace.", "Education"},
	// E-commerce
	{"E-commerce Platform Engineer", "Shopify developer building inventory management and pricing engine.", "E-commerce and retail"},
	// Media
	{"Streaming Platform Engineer", "Video streaming and podcast platform; real-time communication via WebRTC.", "Media and communications"},
	// Gaming
	{"Game Backend Developer", "Multiplayer game backend, game engine integration with Unity and Unreal Engine.", "Gaming"},
	// Hardware
	{"Embedded Systems Engineer", "Firmware for aerospace and satellite sensor systems; FPGA developer.", "Hardware and energy"},
	// Government
	{"GovTech Engineer", "Civic technology and digital government services for public sector.", "Government and public sector"},
	// Agriculture
	{"AgriTech Developer", "Precision farming and crop monitoring software for agribusiness.", "Agriculture"},
	// Architecture
	{"PropTech Engineer", "Real estate platform, BIM developer and smart building systems.", "Architecture and construction"},
	// Photo and video
	{"Video Production Software Engineer", "Video editing, visual effects and post-production tooling for a photography platform.", "Photo and video"},
	// Unclassifiable (no industry keywords)
	{"Office Coordinator", "Manage schedules, correspondence and meeting rooms for the executive team.", ""},
	{"Receptionist", "Greet visitors and handle incoming calls at our front desk.", ""},
	{"Accounts Payable Specialist", "Process invoices and reconcile supplier accounts.", ""},
	{"Facilities Manager", "Oversee building maintenance contracts and vendor relationships.", ""},
	{"Marketing Coordinator", "Coordinate campaigns and social media scheduling for the brand team.", ""},
}

func TestDefaultVocab_CoverageOnFixture(t *testing.T) {
	v := industry.DefaultVocab()

	// Log vocabulary sizes.
	t.Logf("Vocabulary: %d industries", len(v.Industries))
	totalKeywords := 0
	for _, ind := range v.Industries {
		totalKeywords += len(ind.Keywords)
		t.Logf("  %-35s %d keywords", ind.Name, len(ind.Keywords))
	}
	t.Logf("Total keywords: %d", totalKeywords)

	assigned := 0
	for _, f := range fixtureVacancies {
		names := industry.Assign(f.title, "", f.text, v)
		got := ""
		if len(names) > 0 {
			got = names[0]
		}
		if f.want != "" {
			assigned++
			if got != f.want {
				// Allow the vacancy to be assigned to the right industry even if
				// it was also assigned to others; check membership.
				found := false
				for _, n := range names {
					if n == f.want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("vacancy %q: got %v, want industry to include %q", f.title, names, f.want)
				}
			}
		}
	}

	total := len(fixtureVacancies)
	unclassifiable := 0
	for _, f := range fixtureVacancies {
		if f.want == "" {
			unclassifiable++
		}
	}
	var withIndustry int
	for _, f := range fixtureVacancies {
		names := industry.Assign(f.title, "", f.text, v)
		if len(names) > 0 {
			withIndustry++
		}
	}
	withoutIndustry := total - withIndustry
	t.Logf("Coverage: %d/%d have an industry (%.0f%% classified, %.0f%% without)",
		withIndustry, total,
		100*float64(withIndustry)/float64(total),
		100*float64(withoutIndustry)/float64(total))
	t.Logf("Of %d unclassifiable fixture vacancies, %d correctly got no industry",
		unclassifiable, unclassifiable-func() int {
			n := 0
			for _, f := range fixtureVacancies {
				if f.want == "" {
					if names := industry.Assign(f.title, "", f.text, v); len(names) > 0 {
						n++
					}
				}
			}
			return n
		}())

	// The fixture has 29 classified vacancies out of 34 total.
	// Assert that at least 85% of the fixture's classified vacancies are matched.
	classifiedAndMatched := 0
	for _, f := range fixtureVacancies {
		if f.want == "" {
			continue
		}
		names := industry.Assign(f.title, "", f.text, v)
		for _, n := range names {
			if n == f.want {
				classifiedAndMatched++
				break
			}
		}
	}
	classifiedTotal := total - unclassifiable
	if classifiedAndMatched < classifiedTotal {
		t.Errorf("classified coverage: %d/%d vacancies correctly assigned", classifiedAndMatched, classifiedTotal)
	}
}

// --- DefaultVocab ---

func TestDefaultVocab_IsValid(t *testing.T) {
	v := industry.DefaultVocab()
	if err := industry.Validate(v); err != nil {
		t.Errorf("default vocab invalid: %v", err)
	}
}

func TestDefaultVocab_ContainsExpectedIndustries(t *testing.T) {
	v := industry.DefaultVocab()
	names := make(map[string]bool, len(v.Industries))
	for _, ind := range v.Industries {
		names[ind.Name] = true
	}
	want := []string{
		"Software", "Web3", "Security", "Data and AI", "Finance",
		"Healthcare", "Education", "E-commerce and retail",
		"Media and communications", "Gaming", "Hardware and energy",
		"Government and public sector", "Agriculture",
		"Architecture and construction", "Photo and video",
	}
	for _, w := range want {
		if !names[w] {
			t.Errorf("default vocab: missing industry %q", w)
		}
	}
}

// --- Validate ---

func TestValidate_RejectsEmptyVocab(t *testing.T) {
	if err := industry.Validate(industry.Vocab{}); err == nil {
		t.Error("empty vocab: expected error")
	}
}

func TestValidate_RejectsEmptyName(t *testing.T) {
	v := industry.Vocab{
		Industries: []industry.Industry{{Name: "", Keywords: []string{"solidity"}}},
	}
	if err := industry.Validate(v); err == nil {
		t.Error("empty name: expected error")
	}
}

func TestValidate_RejectsDuplicateName(t *testing.T) {
	v := industry.Vocab{
		Industries: []industry.Industry{
			{Name: "Tech", Keywords: []string{"solidity"}},
			{Name: "tech", Keywords: []string{"ethereum"}},
		},
	}
	if err := industry.Validate(v); err == nil {
		t.Error("duplicate name: expected error")
	}
}

func TestValidate_RejectsEmptyKeywordList(t *testing.T) {
	v := industry.Vocab{
		Industries: []industry.Industry{{Name: "Tech", Keywords: nil}},
	}
	if err := industry.Validate(v); err == nil {
		t.Error("empty keywords: expected error")
	}
}

// --- LoadOrDefault and Save ---

func TestLoadOrDefault_UsesDefaultWhenFileAbsent(t *testing.T) {
	v, err := industry.LoadOrDefault(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(v.Industries) == 0 {
		t.Error("expected default vocab, got empty")
	}
}

func TestSaveAndLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vocab", "industries.yaml")
	original := testVocab()

	if err := industry.Save(path, original); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := industry.LoadOrDefault(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Industries) != len(original.Industries) {
		t.Errorf("round trip: got %d industries, want %d", len(loaded.Industries), len(original.Industries))
	}
	for i, ind := range loaded.Industries {
		if ind.Name != original.Industries[i].Name {
			t.Errorf("industry %d: name %q != %q", i, ind.Name, original.Industries[i].Name)
		}
	}
}
