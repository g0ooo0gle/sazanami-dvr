package autoreservation

import (
	"errors"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

func TestProgramMatcherMatchesContentAndComponents(t *testing.T) {
	video := catalogmodel.Video{StreamContent: 1, ComponentType: 0xb3}
	metadata := catalogmodel.ProgramMetadata{
		Genres: []catalogmodel.Genre{
			{Level1: 1, Level2: 2},
			{Level1: 0x0e, Level2: 0, User1: 3, User2: 4},
		},
		Video:  &video,
		Audios: []catalogmodel.Audio{{ComponentType: 3, SamplingRate: 48_000}},
	}
	conditions := []struct {
		name   string
		search SearchCondition
		want   bool
	}{
		{name: "major", search: SearchCondition{Contents: []ContentRange{{Content: wireNibbles(1, 0xff)}}}, want: true},
		{name: "middle", search: SearchCondition{Contents: []ContentRange{{Content: wireNibbles(1, 2)}}}, want: true},
		{name: "wrong middle", search: SearchCondition{Contents: []ContentRange{{Content: wireNibbles(1, 3)}}}},
		{name: "extended user", search: SearchCondition{Contents: []ContentRange{{Content: wireNibbles(0x0e, 0), User: wireNibbles(3, 4)}}}, want: true},
		{name: "wrong extended user", search: SearchCondition{Contents: []ContentRange{{Content: wireNibbles(0x0e, 0), User: wireNibbles(3, 5)}}}},
		{name: "exclude", search: SearchCondition{Contents: []ContentRange{{Content: wireNibbles(1, 2)}}, ExcludeContents: true}},
		{name: "video", search: SearchCondition{Video: []uint16{0x01b3}}, want: true},
		{name: "wrong video", search: SearchCondition{Video: []uint16{0x01b2}}},
		{name: "audio", search: SearchCondition{Audio: []uint16{0x0203}}, want: true},
		{name: "wrong audio", search: SearchCondition{Audio: []uint16{0x0202}}},
	}
	for _, test := range conditions {
		t.Run(test.name, func(t *testing.T) {
			test.search.Enabled = true
			if got := mustMatcher(t, test.search).Matches(programWithMetadata(metadata)); got != test.want {
				t.Fatalf("got=%v want=%v", got, test.want)
			}
		})
	}

	noGenre := SearchCondition{Enabled: true, Contents: []ContentRange{{Content: 0xffff}}}
	if !mustMatcher(t, noGenre).Matches(programWithMetadata(catalogmodel.ProgramMetadata{})) ||
		mustMatcher(t, noGenre).Matches(programWithMetadata(metadata)) {
		t.Fatal("ジャンルなし条件が一致しません")
	}
	videoOnly := SearchCondition{Enabled: true, Video: []uint16{0x01b3}}
	if mustMatcher(t, videoOnly).Matches(programWithMetadata(catalogmodel.ProgramMetadata{})) {
		t.Fatal("metadataなし番組が映像条件に一致しました")
	}
}

func TestFuzzyNormalizationAndDistance(t *testing.T) {
	for _, test := range []struct {
		left, right string
		caseMatch   bool
	}{
		{left: "ＡＢＣ　１２３", right: "abc 123"},
		{left: "ひらがな", right: "ヒラガナ", caseMatch: true},
		{left: "ｶﾞｯﾂﾎﾟｰｽﾞ", right: "ガッツポーズ", caseMatch: true},
		{left: "は\u309a", right: "パ", caseMatch: true},
	} {
		left := normalizeFuzzyText(test.left, test.caseMatch)
		right := normalizeFuzzyText(test.right, test.caseMatch)
		if left != right {
			t.Fatalf("normalize(%q)=%q, normalize(%q)=%q", test.left, left, test.right, right)
		}
	}

	for _, test := range []struct {
		name, target, keyword string
		want                  bool
	}{
		{name: "exact", target: "前テスト番組後", keyword: "テスト番組", want: true},
		{name: "substitution", target: "テスト本組", keyword: "テスト番組", want: true},
		{name: "insertion", target: "テスト新番組", keyword: "テスト番組", want: true},
		{name: "deletion", target: "テス番組", keyword: "テスト番組", want: true},
		{name: "quarter boundary", target: "abXXefgh", keyword: "abcdefgh", want: true},
		{name: "quarter one over", target: "abXXXfgh", keyword: "abcdefgh"},
		{name: "short one over", target: "abXY", keyword: "abcd"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := fuzzyContains(test.target, test.keyword); got != test.want {
				t.Fatalf("got=%v want=%v", got, test.want)
			}
		})
	}
}

func TestProgramMatcherUsesFuzzyOnlyForKeyword(t *testing.T) {
	title, description := "ｶﾞｯﾂ本組", "説明だけの語"
	program := catalogmodel.CurrentProgram{Material: catalogmodel.RevisionMaterial{
		Title: &title, Description: &description,
		Metadata: catalogmodel.ProgramMetadata{Extended: []catalogmodel.ExtendedItem{{Heading: "詳細", Body: "追加の語"}}},
	}}
	search := SearchCondition{Enabled: true, Fuzzy: true, Keyword: "ガッツ番組", Exclude: "本組"}
	if mustMatcher(t, search).Matches(program) {
		t.Fatal("除外語へあいまい検索が適用されました")
	}
	search.Exclude = "存在しない語"
	if !mustMatcher(t, search).Matches(program) {
		t.Fatal("あいまいkeywordが一致しません")
	}
	search.Keyword, search.TitleOnly = "説明だけの話", true
	if mustMatcher(t, search).Matches(program) {
		t.Fatal("title-onlyが概要を検索しました")
	}
	search.Keyword, search.TitleOnly = "追加の話", false
	if !mustMatcher(t, search).Matches(program) {
		t.Fatal("番組詳細が検索対象に含まれません")
	}
}

func TestProgramMatcherMatchesDateDurationAndFreeAccess(t *testing.T) {
	start := time.Date(2026, 8, 9, 0, 30, 0, 0, japanStandardTime).UTC()
	startMS, durationMS := start.UnixMilli(), int64((30*time.Minute)/time.Millisecond)
	title := "番組"
	program := catalogmodel.CurrentProgram{Material: catalogmodel.RevisionMaterial{
		Title: &title, StartUTCMS: &startMS, DurationMS: &durationMS, FreeAccess: catalogmodel.FreeYes,
	}}
	search := SearchCondition{
		Enabled: true, FreeAccess: 1, MinimumMinutes: 30, MaximumMinutes: 30,
		Dates: []DateRange{{StartDay: 6, StartHour: 23, EndDay: 0, EndHour: 1}},
	}
	if !mustMatcher(t, search).Matches(program) {
		t.Fatal("週またぎ、長さ、無料条件の組合せが一致しません")
	}
	search.ExcludeDates = true
	if mustMatcher(t, search).Matches(program) {
		t.Fatal("除外時間帯の番組が一致しました")
	}
	search = SearchCondition{Enabled: true, FreeAccess: 2}
	if mustMatcher(t, search).Matches(program) {
		t.Fatal("無料番組が有料条件に一致しました")
	}
	program.Material.FreeAccess = catalogmodel.FreeNo
	if !mustMatcher(t, search).Matches(program) {
		t.Fatal("有料番組が有料条件に一致しません")
	}
	program.Material.FreeAccess = catalogmodel.FreeUnknown
	if mustMatcher(t, search).Matches(program) {
		t.Fatal("無料状態不明の番組が有料条件に一致しました")
	}
}

func TestPrepareProgramMatcherRejectsInvalidRegexp(t *testing.T) {
	_, err := PrepareProgramMatcher(SearchCondition{Enabled: true, Regex: true, Keyword: "["})
	if !errors.Is(err, ErrInvalidRegexp) || err.Error() != "autoreservation: invalid regexp" {
		t.Fatalf("err=%v", err)
	}
}

func TestProgramMatcherUsesKonomiTVContentByteOrder(t *testing.T) {
	metadata := catalogmodel.ProgramMetadata{Genres: []catalogmodel.Genre{{Level1: 1, Level2: 2}}}
	program := programWithMetadata(metadata)
	middle := SearchCondition{Enabled: true, Contents: []ContentRange{{Content: 0x0201}}}
	major := SearchCondition{Enabled: true, Contents: []ContentRange{{Content: 0xff01}}}
	semanticOrder := SearchCondition{Enabled: true, Contents: []ContentRange{{Content: 0x0102}}}
	if !mustMatcher(t, middle).Matches(program) || !mustMatcher(t, major).Matches(program) ||
		mustMatcher(t, semanticOrder).Matches(program) {
		t.Fatal("KonomiTVが通信路へ書く大分類・中分類のバイト順を解釈できません")
	}
}

func mustMatcher(t *testing.T, search SearchCondition) ProgramMatcher {
	t.Helper()
	matcher, err := PrepareProgramMatcher(search)
	if err != nil {
		t.Fatal(err)
	}
	return matcher
}

func programWithMetadata(metadata catalogmodel.ProgramMetadata) catalogmodel.CurrentProgram {
	title := "番組"
	return catalogmodel.CurrentProgram{Material: catalogmodel.RevisionMaterial{Title: &title, Metadata: metadata}}
}

func wireNibbles(first, second uint8) uint16 {
	return uint16(second)<<8 | uint16(first)
}
