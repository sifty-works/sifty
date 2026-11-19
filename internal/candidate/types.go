package candidate

// MeData is the JSON record stored in the me table's data column.
// It is the canonical serialisation of My data, shared by all profiles.
type MeData struct {
	DisplayName        string              `json:"displayName,omitempty"`
	LegalName          string              `json:"legalName,omitempty"`
	Email              string              `json:"email,omitempty"`
	Phone              string              `json:"phone,omitempty"`
	LinkedIn           string              `json:"linkedin,omitempty"`
	GitHub             string              `json:"github,omitempty"`
	X                  string              `json:"x,omitempty"`
	Website            string              `json:"website,omitempty"`
	Skills             []Skill             `json:"skills,omitempty"`
	AbsentSkills       []AbsentSkill       `json:"absentSkills,omitempty"`
	ConfirmedFacts     []ConfirmedFact     `json:"confirmedFacts,omitempty"`
	Experience         []Experience        `json:"experience,omitempty"`
	Languages          []Language          `json:"languages,omitempty"`
	Availability       *Availability       `json:"availability,omitempty"`
	CurrentLocation    *Location           `json:"currentLocation,omitempty"`
	WorkAuthorizations []WorkAuthorization `json:"workAuthorizations,omitempty"`
	Demographics       *Demographics       `json:"demographics,omitempty"`
}

// Skill is one skill with its proficiency level.
type Skill struct {
	Name   string `json:"name"`
	Level  string `json:"level,omitempty"`
	Source string `json:"source,omitempty"` // where the skill came from (e.g. "manual", "document:N")
}

// AbsentSkill is one skill the user does not have.
type AbsentSkill struct {
	Name string `json:"name"`
}

// ConfirmedFact is a non-skill fact the user has confirmed they meet (have=true)
// or do not meet (have=false), plus free-text facts (empty text field is free-text).
type ConfirmedFact struct {
	Text string `json:"text"`
	Have bool   `json:"have"`
}

// Experience is one role in the work history.
type Experience struct {
	Company string   `json:"company"`
	Title   string   `json:"title"`
	Start   string   `json:"start,omitempty"`
	End     string   `json:"end,omitempty"`
	Current bool     `json:"current,omitempty"`
	Summary string   `json:"summary,omitempty"`
	Skills  []string `json:"skills,omitempty"`
}

// Language is a spoken or written language with its level.
type Language struct {
	Name  string `json:"name"`
	Level string `json:"level,omitempty"`
}

// Availability describes notice period and start date.
type Availability struct {
	NoticeValue *int   `json:"noticeValue"`
	NoticeUnit  string `json:"noticeUnit,omitempty"` // "day" | "week" | "month"
	StartDate   string `json:"startDate,omitempty"`
}

// Location is a country and optional city.
type Location struct {
	Country string `json:"country"`
	City    string `json:"city,omitempty"`
}

// WorkAuthorization describes the user's right to work in one country.
type WorkAuthorization struct {
	Country string `json:"country"`
	Status  string `json:"status,omitempty"` // "eligible" | "need_visa"
}

// Demographics holds the optional application-form-only demographic answers.
// These are never sent to a model.
type Demographics struct {
	Gender            string   `json:"gender,omitempty"`
	GenderDescription string   `json:"genderDescription,omitempty"`
	Race              []string `json:"race,omitempty"`
	RaceDescription   string   `json:"raceDescription,omitempty"`
	Ethnicity         string   `json:"ethnicity,omitempty"`
	Disability        string   `json:"disability,omitempty"`
	VeteranStatus     string   `json:"veteranStatus,omitempty"`
	CriminalRecord    string   `json:"criminalRecord,omitempty"`
}

// Compensation describes the salary and rate expectations of a profile.
type Compensation struct {
	CashMin     *float64  `json:"cashMin"`
	CashTarget  *float64  `json:"cashTarget"`
	Currency    string    `json:"currency,omitempty"`
	GrossNet    string    `json:"grossNet,omitempty"` // "gross" | "net"
	Period      string    `json:"period,omitempty"`   // "month" | "year"
	Crypto      string    `json:"crypto,omitempty"`   // "fiat_only" | "crypto_accepted" | "crypto_only"
	EquityOnTop *bool     `json:"equityOnTop"`
	DayRates    *DayRates `json:"dayRates,omitempty"`
}

// DayRates describes the contract day-rate expectations.
type DayRates struct {
	Min      *float64 `json:"min"`
	Target   *float64 `json:"target"`
	Currency string   `json:"currency,omitempty"`
}

// Hours describes the preferred working hours.
type Hours struct {
	Mode      string   `json:"mode,omitempty"` // "full_time" | "part_time"
	Weekly    *float64 `json:"weekly,omitempty"`
	WeeklyMin *float64 `json:"weeklyMin,omitempty"`
	WeeklyMax *float64 `json:"weeklyMax,omitempty"`
}
