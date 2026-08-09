package recording

import "testing"

func TestComponentModeEffectiveAndExplicit(t *testing.T) {
	tests := []struct {
		mode           ComponentMode
		captions, data bool
	}{
		{ComponentDefault, true, false},
		{ComponentNeither, false, false},
		{ComponentCaptionsOnly, true, false},
		{ComponentDataOnly, false, true},
		{ComponentBoth, true, true},
	}
	for _, test := range tests {
		got := test.mode.Effective()
		if !test.mode.Valid() || got.Captions != test.captions || got.Data != test.data {
			t.Fatalf("mode=%d got=%+v", test.mode, got)
		}
		if test.mode != ComponentDefault && ExplicitComponentMode(test.captions, test.data) != test.mode {
			t.Fatalf("explicit mode=%d", test.mode)
		}
	}
	if ComponentMode(5).Valid() {
		t.Fatal("範囲外のcomponent modeが受理されました")
	}
}
