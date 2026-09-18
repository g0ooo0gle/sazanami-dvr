package mirakurun

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providercatalog "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/catalog"
)

func TestDisplayTextReplacementCharactersAreAccepted(t *testing.T) {
	program, err := decodeProgramText(`{"id":10000200003,"networkId":1,"serviceId":2,"eventId":3,"startAt":1,"duration":1000,"isFree":true,"name":"literal � 日本語🌊é","description":"\uFFFD","extended":{"\uFFFD":"body �"},"unknown":"�"}`)
	if err != nil {
		t.Fatalf("program text with replacement characters: %v", err)
	}
	if program.Title != "literal � 日本語🌊é" || program.Description != "�" || len(program.Extended) != 1 ||
		program.Extended[0].Heading != "�" || program.Extended[0].Body != "body �" {
		t.Fatalf("program=%+v", program)
	}

	service, err := decodeServiceText(`{"id":100002,"networkId":1,"serviceId":2,"name":"局 \uFFFD","type":1}`)
	if err != nil {
		t.Fatalf("catalog service name with replacement character: %v", err)
	}
	if service.DisplayName != "局 �" {
		t.Fatalf("service=%+v", service)
	}

	bootstrap, err := decodeBootstrapServiceText(`{"id":100002,"networkId":1,"serviceId":2,"name":"設定 \uFFFD","type":1}`)
	if err != nil {
		t.Fatalf("bootstrap service name with replacement character: %v", err)
	}
	if bootstrap.Name != "設定 �" {
		t.Fatalf("bootstrap=%+v", bootstrap)
	}
}

func TestDisplayTextDecoderReplacementsArePreserved(t *testing.T) {
	body := append([]byte(`{"id":10000200003,"networkId":1,"serviceId":2,"eventId":3,"startAt":1,"duration":1000,"isFree":true,"name":"`), 0xff)
	body = append(body, []byte(`","description":"\ud800","extended":{"\udc00":"body `)...)
	body = append(body, 0xff)
	body = append(body, []byte(`"},"unknown":{"nested":"\ud800"}}`)...)
	program, err := decodeProgramBytes(body)
	if err != nil {
		t.Fatalf("decoder-generated replacement characters: %v", err)
	}
	if program.Title != "�" || program.Description != "�" || len(program.Extended) != 1 ||
		program.Extended[0].Heading != "�" || program.Extended[0].Body != "body �" {
		t.Fatalf("program=%+v", program)
	}

	serviceBody := append([]byte(`{"id":100002,"networkId":1,"serviceId":2,"name":"`), 0xff)
	serviceBody = append(serviceBody, []byte(`","type":1}`)...)
	service, err := decodeServiceBytes(serviceBody)
	if err != nil || service.DisplayName != "�" {
		t.Fatalf("service=%+v err=%v", service, err)
	}

	bootstrap, err := decodeBootstrapServiceText(`{"id":100002,"networkId":1,"serviceId":2,"name":"\udc00","type":1}`)
	if err != nil || bootstrap.Name != "�" {
		t.Fatalf("bootstrap=%+v err=%v", bootstrap, err)
	}
}

func TestReplacementTextDoesNotRelaxStructuralValidation(t *testing.T) {
	version, err := decodeVersionText(`{"current":"\uFFFD"}`)
	if !provider.IsReason(err, provider.ReasonMalformed) || version.Current != "" {
		t.Fatalf("version=%+v err=%v", version, err)
	}

	base := `"id":10000200003,"networkId":1,"serviceId":2,"eventId":3,"startAt":1,"duration":1000,"isFree":true,"name":"title"`
	for _, test := range []struct {
		name  string
		field string
	}{
		{name: "structure key", field: `"\uFFFD":1`},
		{name: "unknown object key", field: `"unknown":{"\uFFFD":"value"}`},
		{name: "extended duplicate after decoding", field: `"extended":{"\uFFFD":"first","�":"second"}`},
		{name: "video type remains strict", field: `"video":{"type":"�","resolution":"1080i","streamContent":1,"componentType":1}`},
		{name: "audio language remains strict", field: `"audios":[{"componentType":3,"componentTag":1,"isMain":true,"samplingRate":48000,"langs":["�"]}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeProgramText(`{` + base + `,` + test.field + `}`)
			if !provider.IsReason(err, provider.ReasonMalformed) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestDisplayTextNULRemainsUnchanged(t *testing.T) {
	program, err := decodeProgramText(`{"id":10000200003,"networkId":1,"serviceId":2,"eventId":3,"startAt":1,"duration":1000,"isFree":true,"name":"a\u0000b","description":"c\u0000d","extended":{"e\u0000":"f\u0000"}}`)
	if err != nil {
		t.Fatalf("NUL in display text: %v", err)
	}
	if program.Title != "a\x00b" || program.Description != "c\x00d" || len(program.Extended) != 1 ||
		program.Extended[0].Heading != "e\x00" || program.Extended[0].Body != "f\x00" {
		t.Fatalf("program=%+v", program)
	}
}

func TestDisplayTextReplacementStillCountsDecodedBytes(t *testing.T) {
	description := bytes.Repeat([]byte{'a'}, 65_535)
	description = append(description, 0xff)
	body := append([]byte(`{"id":10000200003,"networkId":1,"serviceId":2,"eventId":3,"startAt":1,"duration":1000,"isFree":true,"name":"title","description":"`), description...)
	body = append(body, []byte(`"}`)...)
	_, err := decodeProgramBytes(body)
	if !provider.IsReason(err, provider.ReasonOverLimit) {
		t.Fatalf("error=%v", err)
	}
}

func decodeServiceText(body string) (providercatalog.ServiceObservation, error) {
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	return decodeService(decoder, provider.Provenance{Backend: "MIRAKURUN", Revision: "test"})
}

func decodeServiceBytes(body []byte) (providercatalog.ServiceObservation, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	return decodeService(decoder, provider.Provenance{Backend: "MIRAKURUN", Revision: "test"})
}

func decodeBootstrapServiceText(body string) (BootstrapService, error) {
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	return decodeBootstrapService(decoder)
}

func decodeProgramBytes(body []byte) (providercatalog.ProgramObservation, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	return decodeProgram(decoder, provider.Provenance{Backend: "MIRAKURUN", Revision: "test"})
}

func decodeVersionText(body string) (VersionObservation, error) {
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	return decodeVersion(decoder)
}
