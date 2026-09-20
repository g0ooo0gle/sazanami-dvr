package mpegts

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

func TestParseSDTReadsActualGenerationAndServiceIDs(t *testing.T) {
	section := testSDTSection(0x1234, 0x0001, 7, true, 0, 1,
		testSDTService{ID: 2},
		testSDTService{ID: 3, Descriptors: []byte{0x48, 2, 0x01, 0x02}},
	)
	got, err := ParseSDT(section)
	if err != nil {
		t.Fatal(err)
	}
	want := SDT{
		TransportStreamID: 0x1234, OriginalNetworkID: 1, Version: 7, CurrentNext: true,
		SectionNumber: 0, LastSectionNumber: 1, ServiceIDs: []uint16{2, 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sdt=%+v want=%+v", got, want)
	}
}

func TestParseSDTAcceptsNextGenerationForCallerToIgnore(t *testing.T) {
	got, err := ParseSDT(testSDTSection(1, 2, 3, false, 0, 0, testSDTService{ID: 4}))
	if err != nil || got.CurrentNext || got.ServiceIDs[0] != 4 {
		t.Fatalf("sdt=%+v err=%v", got, err)
	}
}

func TestParseSDTRejectsInvalidStructureAndDuplicateServices(t *testing.T) {
	valid := testSDTSection(1, 2, 0, true, 0, 0, testSDTService{ID: 4})
	tests := []struct {
		name string
		make func() []byte
	}{
		{name: "table id", make: func() []byte {
			section := append([]byte(nil), valid...)
			section[0] = 0x46
			return section
		}},
		{name: "section length", make: func() []byte {
			section := append([]byte(nil), valid...)
			section[2]++
			return section
		}},
		{name: "crc", make: func() []byte {
			section := append([]byte(nil), valid...)
			section[len(section)-1] ^= 1
			return section
		}},
		{name: "section number", make: func() []byte {
			return testSDTSection(1, 2, 0, true, 1, 0, testSDTService{ID: 4})
		}},
		{name: "descriptor boundary", make: func() []byte {
			return testSDTSection(1, 2, 0, true, 0, 0, testSDTService{ID: 4, Descriptors: []byte{0x48, 3, 1}})
		}},
		{name: "duplicate service", make: func() []byte {
			return testSDTSection(1, 2, 0, true, 0, 0, testSDTService{ID: 4}, testSDTService{ID: 4})
		}},
		{name: "section over limit", make: func() []byte {
			return testSDTSection(1, 2, 0, true, 0, 0, testSDTService{ID: 4, Descriptors: bytes.Repeat([]byte{0xaa}, 1005)})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseSDT(test.make()); !errors.Is(err, ErrPSI) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

type testSDTService struct {
	ID          uint16
	Descriptors []byte
}

func testSDTSection(transportID, networkID uint16, version byte, currentNext bool, number, last byte,
	services ...testSDTService,
) []byte {
	current := byte(0)
	if currentNext {
		current = 1
	}
	section := []byte{0x42, 0xb0, 0, byte(transportID >> 8), byte(transportID), 0xc0 | version<<1 | current,
		number, last, byte(networkID >> 8), byte(networkID), 0xff}
	for _, service := range services {
		section = append(section, byte(service.ID>>8), byte(service.ID), 0x00,
			0xf0|byte(len(service.Descriptors)>>8), byte(len(service.Descriptors)))
		section = append(section, service.Descriptors...)
	}
	length := len(section) + 4 - 3
	section[1] = 0xb0 | byte(length>>8)
	section[2] = byte(length)
	crc := CRC32(section)
	return append(section, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}
