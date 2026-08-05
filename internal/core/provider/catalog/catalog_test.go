package catalog

import (
	"testing"
	"time"
)

func TestCloneProgramPageDeepCopiesPointers(t *testing.T) {
	start := time.Now()
	duration := time.Minute
	original := ProgramPage{Items: []ProgramObservation{{Start: &start, Duration: &duration}}}
	clone := CloneProgramPage(original)
	*clone.Items[0].Start = time.Time{}
	*clone.Items[0].Duration = 0
	if original.Items[0].Start.IsZero() || *original.Items[0].Duration == 0 {
		t.Fatal("clone retained pointer fields")
	}
}
