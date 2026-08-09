package catalogmodel

import "testing"

func TestMirakurunSuccessorBoundaries(t *testing.T) {
	eventID := int64(7)
	otherEventID := int64(8)
	start := int64(1_800_000_000_000)
	duration := int64(30 * 60 * 1_000)
	material := func(start, duration *int64) RevisionMaterial {
		return RevisionMaterial{StartUTCMS: start, DurationMS: duration, Validation: ValidationProvisional}
	}
	tests := []struct {
		name                     string
		oldStart, newStart       *int64
		oldDuration, newDuration *int64
		oldEvent, newEvent       *int64
		oldSeen, newSeen         int64
		want                     bool
	}{
		{name: "same", oldStart: &start, newStart: &start, oldDuration: &duration, newDuration: &duration,
			oldEvent: &eventID, newEvent: &eventID, oldSeen: 0, newSeen: 36 * 60 * 60 * 1_000, want: true},
		{name: "shift-limit", oldStart: &start, newStart: pointerInt64(start + 6*60*60*1_000), oldDuration: &duration,
			newDuration: pointerInt64(duration + 6*60*60*1_000), oldEvent: &eventID, newEvent: &eventID, oldSeen: 1, newSeen: 2, want: true},
		{name: "shift-over", oldStart: &start, newStart: pointerInt64(start + 6*60*60*1_000 + 1), oldDuration: &duration,
			newDuration: &duration, oldEvent: &eventID, newEvent: &eventID, oldSeen: 1, newSeen: 2},
		{name: "horizon-over", oldStart: &start, newStart: &start, oldDuration: &duration, newDuration: &duration,
			oldEvent: &eventID, newEvent: &eventID, oldSeen: 0, newSeen: 36*60*60*1_000 + 1},
		{name: "event-mismatch", oldStart: &start, newStart: &start, oldDuration: &duration, newDuration: &duration,
			oldEvent: &eventID, newEvent: &otherEventID, oldSeen: 1, newSeen: 2},
		{name: "event-unknown", oldStart: &start, newStart: &start, oldDuration: &duration, newDuration: &duration,
			oldEvent: nil, newEvent: &eventID, oldSeen: 1, newSeen: 2},
		{name: "time-unknown", oldStart: nil, newStart: &start, oldDuration: &duration, newDuration: &duration,
			oldEvent: &eventID, newEvent: &eventID, oldSeen: 1, newSeen: 2},
		{name: "duration-short", oldStart: &start, newStart: &start, oldDuration: pointerInt64(59_000), newDuration: &duration,
			oldEvent: &eventID, newEvent: &eventID, oldSeen: 1, newSeen: 2},
		{name: "duration-long", oldStart: &start, newStart: &start, oldDuration: &duration,
			newDuration: pointerInt64(12*60*60*1_000 + 1_000), oldEvent: &eventID, newEvent: &eventID, oldSeen: 1, newSeen: 2},
		{name: "duration-fraction", oldStart: &start, newStart: &start, oldDuration: &duration,
			newDuration: pointerInt64(duration + 1), oldEvent: &eventID, newEvent: &eventID, oldSeen: 1, newSeen: 2},
		{name: "clock-backwards", oldStart: &start, newStart: &start, oldDuration: &duration, newDuration: &duration,
			oldEvent: &eventID, newEvent: &eventID, oldSeen: 2, newSeen: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := MirakurunSuccessor(material(test.oldStart, test.oldDuration), test.oldEvent, test.oldSeen,
				material(test.newStart, test.newDuration), test.newEvent, test.newSeen)
			if got != test.want {
				t.Fatalf("got=%v want=%v", got, test.want)
			}
		})
	}
}

func pointerInt64(value int64) *int64 { return &value }
