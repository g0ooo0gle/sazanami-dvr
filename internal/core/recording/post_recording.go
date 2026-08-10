package recording

import (
	"errors"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxPostRecordingScriptBytesは予約に保存できる録画後スクリプトpathのUTF-8 byte上限である。
	MaxPostRecordingScriptBytes = 1_024
)

// PostRecordingModeは録画ファイルを確定した後の動作を表す。
// 既定値と明示的な何もしない設定は、KonomiTVへ同じ選択を読み戻すため区別する。
type PostRecordingMode uint8

const (
	// PostRecordingDefaultは全体既定を使う。v1の実効値は何もしない。
	PostRecordingDefault PostRecordingMode = iota
	// PostRecordingNothingは明示的に何もしない。
	PostRecordingNothing
)

// Validは現在対応する録画後動作かを返す。
func (mode PostRecordingMode) Valid() bool {
	return mode == PostRecordingDefault || mode == PostRecordingNothing
}

// PostRecordingSettingsは予約ごとの録画後動作と任意のスクリプトpathを保持する。
// Scriptの実行可否は、実行環境の許可ディレクトリでも別に検証する。
type PostRecordingSettings struct {
	Mode   PostRecordingMode
	Script string
}

// Validateは通信やファイルシステムに依存しない保存値の上限を検証する。
func (settings PostRecordingSettings) Validate() error {
	if !settings.Mode.Valid() || !utf8.ValidString(settings.Script) || len([]byte(settings.Script)) > MaxPostRecordingScriptBytes {
		return errors.New("recording: invalid post-recording settings")
	}
	for _, value := range settings.Script {
		if value == 0 || unicode.IsControl(value) {
			return errors.New("recording: invalid post-recording script")
		}
	}
	return nil
}
