package industry

import (
	_ "embed"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed labels.yaml
var labelsYAML []byte

type labelEntry struct {
	Label      string   `yaml:"label"`
	Industries []string `yaml:"industries"`
}

type labelsFile struct {
	Labels []labelEntry `yaml:"labels"`
}

var labelsMap map[string][]string

var fixedNames = []string{
	"Software", "Web3", "Security", "Data and AI", "Finance",
	"Healthcare", "Education", "E-commerce and retail",
	"Media and communications", "Gaming", "Hardware and energy",
	"Government and public sector", "Agriculture",
	"Architecture and construction", "Photo and video",
}

func init() {
	var lf labelsFile
	if err := yaml.Unmarshal(labelsYAML, &lf); err != nil {
		panic("industry: cannot parse embedded labels.yaml: " + err.Error())
	}
	labelsMap = make(map[string][]string, len(lf.Labels))
	for _, e := range lf.Labels {
		labelsMap[strings.ToLower(strings.TrimSpace(e.Label))] = e.Industries
	}
}

func Names() []string {
	out := make([]string, len(fixedNames))
	copy(out, fixedNames)
	return out
}

func FromLabels(labels []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, label := range labels {
		key := strings.ToLower(strings.TrimSpace(label))
		if inds, ok := labelsMap[key]; ok {
			for _, ind := range inds {
				if !seen[ind] {
					seen[ind] = true
					out = append(out, ind)
				}
			}
		}
	}
	return out
}
