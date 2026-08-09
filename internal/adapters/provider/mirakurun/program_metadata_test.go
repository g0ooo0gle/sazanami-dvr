package mirakurun

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providercatalog "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/catalog"
)

func TestDecodeProgramMetadata(t *testing.T) {
	body := `{
		"id":10000200003,"networkId":1,"serviceId":2,"eventId":3,
		"startAt":1785628800000,"duration":1800000,"isFree":true,
		"name":"番組","description":"概要",
		"extended":{"見出しB":"本文B","見出しA":"本文A"},
		"genres":[{"lv1":1,"lv2":2,"un1":3,"un2":4,"future":true}],
		"video":{"type":"h.264","resolution":"1080i","streamContent":1,"componentType":179,"future":{}},
		"audios":[{"componentType":3,"componentTag":16,"isMain":true,"samplingRate":48000,"langs":["jpn","eng"],"future":null}],
		"future":{"nested":[1,true,null]}
	}`
	item, err := decodeProgramText(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Extended) != 2 || item.Extended[0].Heading != "見出しB" || len(item.Genres) != 1 ||
		item.Genres[0].User2 != 4 || item.Video == nil || item.Video.ComponentType != 179 ||
		len(item.Audios) != 1 || !item.Audios[0].Main || len(item.Audios[0].Languages) != 2 {
		t.Fatalf("metadata=%+v", item)
	}

	without, err := decodeProgramText(`{"id":10000200003,"networkId":1,"serviceId":2,"eventId":3,"startAt":null,"duration":null,"isFree":false}`)
	if err != nil || len(without.Extended) != 0 || len(without.Genres) != 0 || without.Video != nil || len(without.Audios) != 0 {
		t.Fatalf("項目欠落=%+v err=%v", without, err)
	}
}

func TestDecodeProgramMetadataFailures(t *testing.T) {
	base := `"id":10000200003,"networkId":1,"serviceId":2,"eventId":3,"startAt":1,"duration":1000,"isFree":true`
	genres := make([]string, 65)
	for index := range genres {
		genres[index] = `{"lv1":1,"lv2":2,"un1":3,"un2":4}`
	}
	audios := make([]string, 17)
	for index := range audios {
		audios[index] = `{"componentType":3,"componentTag":1,"isMain":true,"samplingRate":48000,"langs":["jpn"]}`
	}
	extended := make([]string, 65)
	for index := range extended {
		extended[index] = fmt.Sprintf(`"h%d":"b"`, index)
	}
	tests := []struct {
		name   string
		field  string
		reason provider.Reason
	}{
		{name: "extended null", field: `"extended":null`, reason: provider.ReasonMalformed},
		{name: "extended duplicate", field: `"extended":{"a":"b","a":"c"}`, reason: provider.ReasonMalformed},
		{name: "extended value type", field: `"extended":{"a":1}`, reason: provider.ReasonMalformed},
		{name: "extended heading limit", field: `"extended":{"` + strings.Repeat("a", 4097) + `":"b"}`, reason: provider.ReasonOverLimit},
		{name: "extended count", field: `"extended":{` + strings.Join(extended, ",") + `}`, reason: provider.ReasonOverLimit},
		{name: "genres null", field: `"genres":null`, reason: provider.ReasonMalformed},
		{name: "genre missing", field: `"genres":[{"lv1":1,"lv2":2,"un1":3}]`, reason: provider.ReasonMalformed},
		{name: "genre duplicate", field: `"genres":[{"lv1":1,"lv1":1,"lv2":2,"un1":3,"un2":4}]`, reason: provider.ReasonMalformed},
		{name: "genre overflow", field: `"genres":[{"lv1":256,"lv2":2,"un1":3,"un2":4}]`, reason: provider.ReasonOverLimit},
		{name: "genre count", field: `"genres":[` + strings.Join(genres, ",") + `]`, reason: provider.ReasonOverLimit},
		{name: "video null", field: `"video":null`, reason: provider.ReasonMalformed},
		{name: "video missing", field: `"video":{"type":"h.264","resolution":"1080i","streamContent":1}`, reason: provider.ReasonMalformed},
		{name: "video wrong type", field: `"video":{"type":1,"resolution":"1080i","streamContent":1,"componentType":2}`, reason: provider.ReasonMalformed},
		{name: "audio null", field: `"audios":null`, reason: provider.ReasonMalformed},
		{name: "audio missing", field: `"audios":[{"componentType":3,"componentTag":1,"isMain":true,"samplingRate":48000}]`, reason: provider.ReasonMalformed},
		{name: "audio sampling", field: `"audios":[{"componentType":3,"componentTag":1,"isMain":true,"samplingRate":47999,"langs":[]}]`, reason: provider.ReasonMalformed},
		{name: "audio language", field: `"audios":[{"componentType":3,"componentTag":1,"isMain":true,"samplingRate":48000,"langs":["und"]}]`, reason: provider.ReasonMalformed},
		{name: "audio language count", field: `"audios":[{"componentType":3,"componentTag":1,"isMain":true,"samplingRate":48000,"langs":["jpn","eng","etc"]}]`, reason: provider.ReasonOverLimit},
		{name: "audio count", field: `"audios":[` + strings.Join(audios, ",") + `]`, reason: provider.ReasonOverLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeProgramText(`{` + base + `,` + test.field + `}`)
			if !provider.IsReason(err, test.reason) {
				t.Fatalf("error=%v want=%s", err, test.reason)
			}
		})
	}
}

func decodeProgramText(body string) (providercatalog.ProgramObservation, error) {
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	return decodeProgram(decoder, provider.Provenance{Backend: "MIRAKURUN", Revision: "test"})
}
