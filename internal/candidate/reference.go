package candidate

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed reference.json
var referenceJSON []byte

// ReferenceCountry is one country entry in the reference data.
type ReferenceCountry struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// ReferenceRegion is one region (e.g. EU) with its member countries.
type ReferenceRegion struct {
	Code      string   `json:"code"`
	Name      string   `json:"name"`
	Size      int      `json:"size"`
	Aliases   []string `json:"aliases"`
	Countries []string `json:"countries"`
}

// ReferenceTimeZoneBand is one UTC time-zone band.
type ReferenceTimeZoneBand struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// OptionItem is a value-label pair for select inputs.
type OptionItem struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// ReferenceOptions holds every option set used in the editors.
type ReferenceOptions struct {
	SkillLevels    []OptionItem `json:"skillLevels"`
	LanguageLevels []OptionItem `json:"languageLevels"`
	Currencies     []OptionItem `json:"currencies"`
	Periods        []OptionItem `json:"periods"`
	PaymentMethods []OptionItem `json:"paymentMethods"`
	Engagement     []OptionItem `json:"engagement"`
	WorkingTime    []OptionItem `json:"workingTime"`
	WorkModes      []OptionItem `json:"workModes"`
	Relocation     []OptionItem `json:"relocation"`
	NoticeUnits    []OptionItem `json:"noticeUnits"`
	Gender         []OptionItem `json:"gender"`
	Race           []OptionItem `json:"race"`
	Ethnicity      []OptionItem `json:"ethnicity"`
	Disability     []OptionItem `json:"disability"`
	VeteranStatus  []OptionItem `json:"veteranStatus"`
	CriminalRecord []OptionItem `json:"criminalRecord"`
}

// ReferenceResp is the response for GET /api/reference.
type ReferenceResp struct {
	Countries     []ReferenceCountry      `json:"countries"`
	Regions       []ReferenceRegion       `json:"regions"`
	TimeZoneBands []ReferenceTimeZoneBand `json:"timeZoneBands"`
	Options       ReferenceOptions        `json:"options"`
}

var referenceData = mustLoadReference()

func mustLoadReference() ReferenceResp {
	var r ReferenceResp
	if err := json.Unmarshal(referenceJSON, &r); err != nil {
		panic(fmt.Sprintf("candidate: load reference.json: %v", err))
	}
	return r
}
