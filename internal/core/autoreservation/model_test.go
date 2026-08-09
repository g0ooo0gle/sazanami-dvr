package autoreservation

import (
	"strings"
	"testing"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

func TestRuleValidation(t *testing.T) {
	id := catalogmodel.ID{1}
	rule := Rule{
		ID: id, Version: 1, CreatedAtUTCMS: 1, UpdatedAtUTCMS: 1,
		Search:    SearchCondition{Enabled: true},
		Recording: RecordingSettings{Mode: 1, Priority: 3, Follow: true},
	}
	if err := rule.ValidateNew(); err != nil {
		t.Fatal(err)
	}
	rule.Number = 1
	if err := rule.ValidateStored(); err != nil {
		t.Fatal(err)
	}

	cases := []Rule{rule, rule, rule, rule, rule}
	cases[0].Search.Keyword = strings.Repeat("a", maxTextBytes+1)
	cases[1].Search.Dates = []DateRange{{StartDay: 7}}
	cases[2].Recording.Priority = 0
	cases[3].Recording.StartMargin = new(int32)
	cases[4].Recording.Folders = make([]Folder, maxFolders+1)
	for index, invalid := range cases {
		if err := invalid.ValidateStored(); err == nil {
			t.Fatalf("case %d accepted", index)
		}
	}
}

func TestValidateChange(t *testing.T) {
	search := SearchCondition{MinimumMinutes: 60, MaximumMinutes: 30}
	settings := RecordingSettings{Mode: 1, Priority: 3}
	if err := ValidateChange(1, search, settings); err == nil {
		t.Fatal("inverted duration accepted")
	}
	search.MinimumMinutes = 30
	search.MaximumMinutes = 60
	if err := ValidateChange(1, search, settings); err != nil {
		t.Fatal(err)
	}
}
