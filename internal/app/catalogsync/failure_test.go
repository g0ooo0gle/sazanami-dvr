package catalogsync

import (
	"errors"
	"testing"
)

func TestFailureStagePreservesCause(t *testing.T) {
	cause := errors.New("synthetic")
	for _, value := range []FailureStage{FailureInternal, FailureProvider, FailureStore, FailureValidation} {
		err := stage(value, cause)
		if StageOf(err) != value || !errors.Is(err, cause) || err.Error() != cause.Error() {
			t.Fatalf("stage=%d err=%v", value, err)
		}
		if stage(FailureInternal, err) != err {
			t.Fatalf("stage=%d was wrapped twice", value)
		}
	}
	if StageOf(cause) != FailureInternal || stage(FailureStore, nil) != nil {
		t.Fatal("unclassified or nil error was handled incorrectly")
	}
}
