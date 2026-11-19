package candidate

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/sifty-works/sifty-server/internal/places"
)

//go:embed options.json
var optionsJSON []byte

// OptionItem is a value-label pair for select inputs.
type OptionItem struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// ReferenceOptions holds every option set used in the editors.
type ReferenceOptions struct {
	SkillLevels       []OptionItem `json:"skillLevels"`
	LanguageLevels    []OptionItem `json:"languageLevels"`
	Currencies        []OptionItem `json:"currencies"`
	Periods           []OptionItem `json:"periods"`
	PaymentMethods    []OptionItem `json:"paymentMethods"`
	Engagement        []OptionItem `json:"engagement"`
	WorkingTime       []OptionItem `json:"workingTime"`
	WorkModes         []OptionItem `json:"workModes"`
	Relocation        []OptionItem `json:"relocation"`
	NoticeUnits       []OptionItem `json:"noticeUnits"`
	Gender            []OptionItem `json:"gender"`
	Race              []OptionItem `json:"race"`
	Ethnicity         []OptionItem `json:"ethnicity"`
	Disability        []OptionItem `json:"disability"`
	VeteranStatus     []OptionItem `json:"veteranStatus"`
	CriminalRecord    []OptionItem `json:"criminalRecord"`
	WorkAuthorization []OptionItem `json:"workAuthorization"`
}

// ReferenceResp is the response for GET /api/reference: the places vocabulary
// and the option sets of the editors.
type ReferenceResp struct {
	Countries     []places.ReferenceCountry      `json:"countries"`
	Regions       []places.ReferenceRegion       `json:"regions"`
	TimeZoneBands []places.ReferenceTimeZoneBand `json:"timeZoneBands"`
	Options       ReferenceOptions               `json:"options"`
}

var referenceData = mustLoadReference()

func mustLoadReference() ReferenceResp {
	r := ReferenceResp{Countries: places.Countries(), Regions: places.Regions(), TimeZoneBands: places.Bands()}
	if err := json.Unmarshal(optionsJSON, &r); err != nil {
		panic(fmt.Sprintf("candidate: load options.json: %v", err))
	}
	return r
}
