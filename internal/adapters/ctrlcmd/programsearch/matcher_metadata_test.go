package programsearch

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/channel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

func TestPreparedConditionMatchesContentAndComponents(t *testing.T) {
	video := catalogmodel.Video{StreamContent: 1, ComponentType: 0xb3}
	metadata := catalogmodel.ProgramMetadata{
		Genres: []catalogmodel.Genre{
			{Level1: 1, Level2: 2},
			{Level1: 0x0e, Level2: 0, User1: 3, User2: 4},
		},
		Video:  &video,
		Audios: []catalogmodel.Audio{{ComponentType: 3, SamplingRate: 48_000}},
	}
	material := catalogmodel.RevisionMaterial{Metadata: metadata}
	conditions := []struct {
		name   string
		search core.SearchCondition
		want   bool
	}{
		{name: "major", search: core.SearchCondition{Contents: []core.ContentRange{{Content: wireNibbles(1, 0xff)}}}, want: true},
		{name: "middle", search: core.SearchCondition{Contents: []core.ContentRange{{Content: wireNibbles(1, 2)}}}, want: true},
		{name: "wrong middle", search: core.SearchCondition{Contents: []core.ContentRange{{Content: wireNibbles(1, 3)}}}},
		{name: "extended user", search: core.SearchCondition{Contents: []core.ContentRange{{Content: wireNibbles(0x0e, 0), User: wireNibbles(3, 4)}}}, want: true},
		{name: "wrong extended user", search: core.SearchCondition{Contents: []core.ContentRange{{Content: wireNibbles(0x0e, 0), User: wireNibbles(3, 5)}}}},
		{name: "exclude", search: core.SearchCondition{Contents: []core.ContentRange{{Content: wireNibbles(1, 2)}}, ExcludeContents: true}},
		{name: "video", search: core.SearchCondition{Video: []uint16{0x01b3}}, want: true},
		{name: "wrong video", search: core.SearchCondition{Video: []uint16{0x01b2}}},
		{name: "audio", search: core.SearchCondition{Audio: []uint16{0x0203}}, want: true},
		{name: "wrong audio", search: core.SearchCondition{Audio: []uint16{0x0202}}},
	}
	for _, test := range conditions {
		t.Run(test.name, func(t *testing.T) {
			prepared := preparedCondition{search: test.search}
			got := prepared.matchesContents(material.Metadata) && prepared.matchesComponents(material.Metadata)
			if got != test.want {
				t.Fatalf("got=%v want=%v", got, test.want)
			}
		})
	}

	noGenre := preparedCondition{search: core.SearchCondition{Contents: []core.ContentRange{{Content: 0xffff}}}}
	if !noGenre.matchesContents(catalogmodel.ProgramMetadata{}) || noGenre.matchesContents(metadata) {
		t.Fatal("ジャンルなし条件が一致しません")
	}
	if (preparedCondition{search: core.SearchCondition{Video: []uint16{0x01b3}}}).matchesComponents(catalogmodel.ProgramMetadata{}) {
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

func TestFuzzyAppliesOnlyToKeywordAndHonorsTitleOnly(t *testing.T) {
	title, description := "ｶﾞｯﾂ本組", "説明だけの語"
	material := catalogmodel.RevisionMaterial{Title: &title, Description: &description,
		Metadata: catalogmodel.ProgramMetadata{Extended: []catalogmodel.ExtendedItem{{Heading: "詳細", Body: "追加の語"}}}}
	prepared := preparedCondition{search: core.SearchCondition{Fuzzy: true, Keyword: "ガッツ番組", Exclude: "本組"}}
	if prepared.matchesText(material) {
		t.Fatal("除外語へあいまい検索が適用されました")
	}
	prepared.search.Exclude = "存在しない語"
	if !prepared.matchesText(material) {
		t.Fatal("あいまいkeywordが一致しません")
	}
	prepared.search.Keyword, prepared.search.TitleOnly = "説明だけの話", true
	if prepared.matchesText(material) {
		t.Fatal("title-onlyが概要を検索しました")
	}
	prepared.search.Keyword, prepared.search.TitleOnly = "追加の話", false
	if !prepared.matchesText(material) {
		t.Fatal("番組詳細が検索対象に含まれません")
	}
}

func TestHandlerAppliesMetadataAndFuzzyConditions(t *testing.T) {
	service := searchService("1003", 1, 2, 3)
	matched := searchProgram("1003", "event:1", 10, time.Now().UTC(), "テスト本組", "", true)
	video := catalogmodel.Video{StreamContent: 1, ComponentType: 0xb3}
	matched.Material.Metadata = catalogmodel.ProgramMetadata{
		Genres: []catalogmodel.Genre{{Level1: 1, Level2: 2}}, Video: &video,
		Audios: []catalogmodel.Audio{{ComponentType: 3, SamplingRate: 48_000}},
	}
	unmatched := searchProgram("1003", "event:2", 11, time.Now().UTC(), "別番組", "", true)
	search := core.SearchCondition{
		Enabled: true, Fuzzy: true, Keyword: "テスト番組",
		Services: []core.ServiceRange{{NetworkID: 1, TransportStreamID: 2, ServiceID: 3}},
		Contents: []core.ContentRange{{Content: wireNibbles(1, 0xff)}}, Video: []uint16{0x01b3}, Audio: []uint16{0x0203},
	}
	source := &memorySource{snapshot: channel.Snapshot{Key: "metadata", Services: []channel.Service{service}}, programs: []catalogmodel.CurrentProgram{matched, unmatched}}
	handler, err := NewHandler(source, codec.DefaultLimits(), make(chan struct{}, 1))
	if err != nil {
		t.Fatal(err)
	}
	var response bytes.Buffer
	if err := handler.Handle(context.Background(), searchRequest(t, search, ""), &response); err != nil {
		t.Fatal(err)
	}
	ids := responseEventIDs(t, response.Bytes())
	if len(ids) != 1 || ids[0] != 10 {
		t.Fatalf("ids=%v", ids)
	}
}

func wireNibbles(first, second uint8) uint16 {
	return uint16(second)<<8 | uint16(first)
}
