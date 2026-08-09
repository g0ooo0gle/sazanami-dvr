package recording

// ComponentModeは予約で選んだ字幕とデータ放送の扱いを表す。
// 既定値と明示指定を区別し、CtrlCmdへ利用者の選択をそのまま返せるようにする。
type ComponentMode uint8

const (
	// ComponentDefaultは全体既定の字幕あり、データ放送なしを使う。
	ComponentDefault ComponentMode = iota
	// ComponentNeitherは字幕とデータ放送をどちらも含めない。
	ComponentNeither
	// ComponentCaptionsOnlyは字幕だけを含める。
	ComponentCaptionsOnly
	// ComponentDataOnlyはデータ放送だけを含める。
	ComponentDataOnly
	// ComponentBothは字幕とデータ放送を両方含める。
	ComponentBoth
)

// RecordingComponentsは録画へ実際に含める二つのcomponentを表す。
type RecordingComponents struct {
	Captions bool
	Data     bool
}

// ValidはDBへ保存できる固定値かを返す。
func (mode ComponentMode) Valid() bool {
	return mode >= ComponentDefault && mode <= ComponentBoth
}

// Effectiveは全体既定を解決し、録画で使う実効値を返す。
func (mode ComponentMode) Effective() RecordingComponents {
	switch mode {
	case ComponentNeither:
		return RecordingComponents{}
	case ComponentCaptionsOnly:
		return RecordingComponents{Captions: true}
	case ComponentDataOnly:
		return RecordingComponents{Data: true}
	case ComponentBoth:
		return RecordingComponents{Captions: true, Data: true}
	default:
		return RecordingComponents{Captions: true}
	}
}

// ExplicitComponentModeは字幕とデータ放送の明示指定を固定値へ変換する。
func ExplicitComponentMode(captions, data bool) ComponentMode {
	switch {
	case captions && data:
		return ComponentBoth
	case captions:
		return ComponentCaptionsOnly
	case data:
		return ComponentDataOnly
	default:
		return ComponentNeither
	}
}
