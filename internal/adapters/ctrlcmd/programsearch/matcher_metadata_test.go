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
		Contents: []core.ContentRange{{Content: uint16(0xff)<<8 | 1}}, Video: []uint16{0x01b3}, Audio: []uint16{0x0203},
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
