package recording

import (
	"strings"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

func TestOutputSettingsValidate(t *testing.T) {
	valid := []OutputSettings{
		{},
		{Folder: "番組/保存"},
		{Template: "$Title$.ts"},
		{Folder: strings.Repeat("a", MaxOutputFolderBytes), Template: strings.Repeat("x", MaxOutputTemplateBytes)},
	}
	for _, settings := range valid {
		if err := settings.Validate(); err != nil {
			t.Fatalf("有効な出力設定が拒否されました: %+v: %v", settings, err)
		}
	}

	invalid := []OutputSettings{
		{Folder: "/absolute"},
		{Folder: "../parent"},
		{Folder: "current/./item"},
		{Folder: "empty//item"},
		{Folder: "trailing/"},
		{Folder: `windows\path`},
		{Folder: "C:drive"},
		{Folder: "control\x00"},
		{Folder: strings.Repeat("a", MaxOutputFolderBytes+1)},
		{Template: "$Unknown$"},
		{Template: "$Title"},
		{Template: "$Title(F())$"},
		{Template: "sub/path.ts"},
		{Template: `sub\path.ts`},
		{Template: "control\n.ts"},
		{Template: strings.Repeat("x", MaxOutputTemplateBytes+1)},
	}
	for _, settings := range invalid {
		if err := settings.Validate(); err == nil {
			t.Fatalf("不正な出力設定を受理しました: %+v", settings)
		}
	}
}

func TestNewReservationFilePlanExpandsSupportedMacros(t *testing.T) {
	reservation := outputReservation(t)
	reservation.Output = OutputSettings{
		Folder:   "番組/保存",
		Template: "$SDYYYY$-$SDMM$-$SDDD$_$STHH$$STMM$_$Title$_$ReserveID$",
	}
	plan, err := NewReservationFilePlan(reservation, idForTest(t, 90))
	if err != nil {
		t.Fatal(err)
	}
	const final = "番組/保存/2026-08-11_0001_[新]番組_名_42.ts"
	if plan.FinalPath != final || plan.PartialPath != final+".partial" {
		t.Fatalf("plan=%+v", plan)
	}
	if scheduled, ok, err := ScheduledOutputPath(reservation); err != nil || !ok || scheduled != final {
		t.Fatalf("scheduled=%q ok=%v err=%v", scheduled, ok, err)
	}
}

func TestOutputMacroValues(t *testing.T) {
	reservation := outputReservation(t)
	tests := map[string]string{
		"$Title$":       "[新]番組_名.ts",
		"$Title2$":      "番組_名.ts",
		"$ServiceName$": "放送_局.ts",
		"$SDYYYY$":      "2026.ts",
		"$SDYY$":        "26.ts",
		"$SDMM$":        "08.ts",
		"$SDM$":         "8.ts",
		"$SDDD$":        "11.ts",
		"$SDD$":         "11.ts",
		"$SDW$":         "火.ts",
		"$STHH$":        "00.ts",
		"$STH$":         "0.ts",
		"$STMM$":        "01.ts",
		"$STM$":         "1.ts",
		"$STSS$":        "02.ts",
		"$STS$":         "2.ts",
		"$EDYYYY$":      "2026.ts",
		"$EDYY$":        "26.ts",
		"$EDMM$":        "08.ts",
		"$EDM$":         "8.ts",
		"$EDDD$":        "11.ts",
		"$EDD$":         "11.ts",
		"$EDW$":         "火.ts",
		"$ETHH$":        "01.ts",
		"$ETH$":         "1.ts",
		"$ETMM$":        "03.ts",
		"$ETM$":         "3.ts",
		"$ETSS$":        "05.ts",
		"$ETS$":         "5.ts",
		"$DUHH$":        "01.ts",
		"$DUH$":         "1.ts",
		"$DUMM$":        "02.ts",
		"$DUM$":         "2.ts",
		"$DUSS$":        "03.ts",
		"$DUS$":         "3.ts",
		"$ONID10$":      "1.ts",
		"$TSID10$":      "2.ts",
		"$SID10$":       "3.ts",
		"$EID10$":       "4.ts",
		"$ONID16$":      "0001.ts",
		"$TSID16$":      "0002.ts",
		"$SID16$":       "0003.ts",
		"$EID16$":       "0004.ts",
		"$ReserveID$":   "42.ts",
	}
	for template, want := range tests {
		reservation.Output.Template = template
		got, err := expandOutputName(reservation)
		if err != nil || got != want {
			t.Errorf("template=%s got=%q want=%q err=%v", template, got, want, err)
		}
	}
}

func TestOutputNameBoundariesAndDefaultPlan(t *testing.T) {
	reservation := outputReservation(t)
	reservation.Output = OutputSettings{Folder: "custom"}
	plan, err := NewReservationFilePlan(reservation, idForTest(t, 91))
	if err != nil || !strings.HasPrefix(plan.FinalPath, "custom/") || !strings.HasSuffix(plan.FinalPath, ".ts") {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if value, ok, err := ScheduledOutputPath(reservation); err != nil || ok || value != "" {
		t.Fatalf("scheduled=%q ok=%v err=%v", value, ok, err)
	}

	reservation.Output.Template = strings.Repeat("a", MaxOutputNameBytes-3)
	if _, err := expandOutputName(reservation); err != nil {
		t.Fatalf("240バイトの名前が失敗しました: %v", err)
	}
	reservation.Output.Template = strings.Repeat("a", MaxOutputNameBytes-2)
	if _, err := expandOutputName(reservation); err == nil {
		t.Fatal("241バイトの名前を受理しました")
	}
}

func TestOneSegFilePlanUsesDedicatedSuffixAndOutput(t *testing.T) {
	reservation := outputReservation(t)
	reservation.Output = OutputSettings{Folder: "main", Template: "main.ts"}
	reservation.OneSegOutput = &OneSegOutput{
		ProviderServiceLocator: "1004",
		Output:                 OutputSettings{Folder: "partial", Template: "program.TS"},
	}
	plan, err := NewOneSegFilePlan(reservation, catalogmodel.ID{1})
	if err != nil {
		t.Fatal(err)
	}
	if plan.FinalPath != "partial/program.oneseg.TS" || plan.PartialPath != "partial/program.oneseg.TS.partial" {
		t.Fatalf("plan=%+v", plan)
	}

	reservation.OneSegOutput.Output.Template = "program.oneseg.ts"
	plan, err = NewOneSegFilePlan(reservation, catalogmodel.ID{1})
	if err != nil || plan.FinalPath != "partial/program.oneseg.oneseg.ts" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}

func TestOneSegFilePlanRechecksExpandedNameLimit(t *testing.T) {
	reservation := outputReservation(t)
	reservation.OneSegOutput = &OneSegOutput{
		ProviderServiceLocator: "1004",
		Output:                 OutputSettings{Template: strings.Repeat("a", MaxOutputNameBytes-3)},
	}
	if _, err := NewOneSegFilePlan(reservation, catalogmodel.ID{1}); err == nil {
		t.Fatal("ワンセグsuffix追加後の上限超過を受理しました")
	}
}

func TestTitle2KeepsUnmatchedBracket(t *testing.T) {
	if got := withoutSquareBrackets("番組[新]本編[未完"); got != "番組本編[未完" {
		t.Fatalf("got=%q", got)
	}
}

func outputReservation(t *testing.T) Reservation {
	t.Helper()
	return Reservation{
		Number: 42,
		Program: ProgramSnapshot{
			NetworkID: 1, TransportStreamID: 2, ServiceID: 3, EventID: 4,
			Title: "[新]番組/名", StationName: "放送:局",
			Start: time.Date(2026, 8, 10, 15, 1, 2, 0, time.UTC), Duration: time.Hour + 2*time.Minute + 3*time.Second,
		},
	}
}
