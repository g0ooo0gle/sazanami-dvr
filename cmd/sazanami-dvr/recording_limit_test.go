package main

import "testing"

func TestRecordingStreamLimit(t *testing.T) {
	maximumInt := int(^uint(0) >> 1)
	for _, test := range []struct {
		name       string
		recordings int
		want       int
		wantError  bool
	}{
		{name: "one", recordings: 1, want: 2},
		{name: "two", recordings: 2, want: 4},
		{name: "twenty", recordings: 20, want: 40},
		{name: "largest safe", recordings: maximumInt / 2, want: maximumInt - 1},
		{name: "overflow", recordings: maximumInt/2 + 1, wantError: true},
		{name: "zero", wantError: true},
		{name: "negative", recordings: -1, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := recordingStreamLimit(test.recordings)
			if (err != nil) != test.wantError || got != test.want {
				t.Fatalf("limit=%d err=%v", got, err)
			}
		})
	}
}
