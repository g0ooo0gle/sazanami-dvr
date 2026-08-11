package recording

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

const (
	// MaxOutputFolderBytesは録画保存ルートからの相対フォルダーに許すUTF-8バイト数である。
	MaxOutputFolderBytes = 256
	// MaxOutputTemplateBytesはCtrlCmdから保存するファイル名テンプレートのUTF-8バイト数である。
	MaxOutputTemplateBytes = 512
	// MaxOutputNameBytesは展開後の一つのTSファイル名に許すUTF-8バイト数である。
	MaxOutputNameBytes = 240
)

var japanStandardTime = time.FixedZone("Asia/Tokyo", 9*60*60)

// OutputSettingsは録画保存ルート内の相対フォルダーと、予約情報だけで展開するファイル名規則である。
// 両方が空なら従来の年月フォルダーと録画処理IDを使う。
type OutputSettings struct {
	Folder   string
	Template string
}

// OneSegOutputは予約時点で固定したワンセグサービスと保存先である。
// 接続先は正規化済みの正の10進文字列として保持し、数値IDを予約の主キーには使わない。
type OneSegOutput struct {
	ProviderServiceLocator string
	Output                 OutputSettings
}

// Validateは接続先と保存先を安全に永続化できるか確認する。
func (output OneSegOutput) Validate() error {
	parsed, err := strconv.ParseUint(output.ProviderServiceLocator, 10, 63)
	if !validText(output.ProviderServiceLocator, 1, 256) || err != nil || parsed == 0 ||
		strconv.FormatUint(parsed, 10) != output.ProviderServiceLocator || output.Output.Validate() != nil {
		return errors.New("recording: invalid one-seg output")
	}
	return nil
}

// ResolveOneSegOutputは有効要求へ解決済み接続先を結び付ける。
// 空の保存先はメイン設定を継承する指定としてそのまま固定する。
func ResolveOneSegOutput(request ReservationRequest, providerServiceLocator string) (*OneSegOutput, error) {
	if request.OneSegOutput == nil {
		return nil, nil
	}
	result := &OneSegOutput{ProviderServiceLocator: providerServiceLocator, Output: *request.OneSegOutput}
	if result.Validate() != nil {
		return nil, errors.New("recording: invalid resolved one-seg output")
	}
	return result, nil
}

// Validateは外部pathや未対応マクロへ解決されない要求値かを確認する。
func (settings OutputSettings) Validate() error {
	if settings.Folder != "" && !validOutputFolder(settings.Folder) {
		return errors.New("recording: invalid output folder")
	}
	if settings.Template != "" && !validOutputTemplate(settings.Template) {
		return errors.New("recording: invalid output template")
	}
	return nil
}

// NewReservationFilePlanは予約の出力指定を一回の録画処理で使う部分・完成相対pathへ固定する。
func NewReservationFilePlan(reservation Reservation, attemptID catalogmodel.ID) (FilePlan, error) {
	if attemptID == (catalogmodel.ID{}) || reservation.Output.Validate() != nil ||
		reservation.Program.Start.IsZero() || reservation.Program.Start.Location() != time.UTC {
		return FilePlan{}, errors.New("recording: invalid reservation file plan source")
	}
	directory := reservation.Output.Folder
	if directory == "" {
		directory = fmt.Sprintf("%04d/%02d", reservation.Program.Start.Year(), int(reservation.Program.Start.Month()))
	}
	name := attemptID.String() + ".ts"
	if reservation.Output.Template != "" {
		if reservation.Number < 1 {
			return FilePlan{}, errors.New("recording: missing reservation number for output template")
		}
		var err error
		name, err = expandOutputName(reservation)
		if err != nil {
			return FilePlan{}, err
		}
	}
	plan := FilePlan{PartialPath: directory + "/" + name + ".partial", FinalPath: directory + "/" + name}
	if err := plan.Validate(); err != nil {
		return FilePlan{}, errors.New("recording: invalid generated reservation file plan")
	}
	return plan, nil
}

// NewOneSegFilePlanはワンセグ用の保存先を使い、完成名の拡張子直前へ`.oneseg`を一度加える。
func NewOneSegFilePlan(reservation Reservation, attemptID catalogmodel.ID) (FilePlan, error) {
	if reservation.OneSegOutput == nil || reservation.OneSegOutput.Validate() != nil {
		return FilePlan{}, errors.New("recording: invalid one-seg file plan source")
	}
	oneSeg := reservation
	oneSeg.Output = reservation.OneSegOutput.Output
	if oneSeg.Output == (OutputSettings{}) {
		oneSeg.Output = reservation.Output
	}
	plan, err := NewReservationFilePlan(oneSeg, attemptID)
	if err != nil {
		return FilePlan{}, err
	}
	name := path.Base(plan.FinalPath)
	if len(name) < 3 || !strings.EqualFold(name[len(name)-3:], ".ts") {
		return FilePlan{}, errors.New("recording: invalid one-seg file extension")
	}
	name = name[:len(name)-3] + ".oneseg" + name[len(name)-3:]
	if !validOutputName(name) {
		return FilePlan{}, errors.New("recording: invalid one-seg output name")
	}
	directory := path.Dir(plan.FinalPath)
	plan.FinalPath = directory + "/" + name
	plan.PartialPath = plan.FinalPath + ".partial"
	if err := plan.Validate(); err != nil {
		return FilePlan{}, errors.New("recording: invalid generated one-seg file plan")
	}
	return plan, nil
}

// ScheduledOutputPathはテンプレートを持つ予約について、2011へ返す実録画と同じ予定相対pathを作る。
// boolがfalseなら録画処理IDの確定前に予定名を作れない既定設定である。
func ScheduledOutputPath(reservation Reservation) (string, bool, error) {
	if reservation.Output.Validate() != nil {
		return "", false, errors.New("recording: invalid scheduled output settings")
	}
	if reservation.Output.Template == "" {
		return "", false, nil
	}
	if reservation.Number < 1 {
		return "", false, errors.New("recording: missing reservation number for scheduled output")
	}
	name, err := expandOutputName(reservation)
	if err != nil {
		return "", false, err
	}
	directory := reservation.Output.Folder
	if directory == "" {
		directory = fmt.Sprintf("%04d/%02d", reservation.Program.Start.Year(), int(reservation.Program.Start.Month()))
	}
	value := directory + "/" + name
	if !validRelativePath(value) {
		return "", false, errors.New("recording: invalid scheduled output path")
	}
	return value, true, nil
}

// validOutputFolderはOSの絶対pathへ解釈されない正規の相対フォルダーだけを受ける。
func validOutputFolder(value string) bool {
	if !utf8.ValidString(value) || len(value) == 0 || len(value) > MaxOutputFolderBytes ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "\\") ||
		strings.Contains(value, ":") || path.Clean(value) != value {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." || containsControl(part) {
			return false
		}
	}
	return true
}

// validOutputTemplateは一つのファイル名だけを作る対応マクロと文字列かを事前検証する。
func validOutputTemplate(value string) bool {
	if !utf8.ValidString(value) || len(value) == 0 || len(value) > MaxOutputTemplateBytes ||
		strings.ContainsAny(value, "/\\") || containsControl(value) {
		return false
	}
	for offset := 0; offset < len(value); {
		start := strings.IndexByte(value[offset:], '$')
		if start < 0 {
			return true
		}
		start += offset
		end := strings.IndexByte(value[start+1:], '$')
		if end < 0 {
			return false
		}
		end += start + 1
		if !supportedOutputMacro(value[start+1 : end]) {
			return false
		}
		offset = end + 1
	}
	return true
}

func supportedOutputMacro(name string) bool {
	switch name {
	case "Title", "Title2", "ServiceName",
		"SDYYYY", "SDYY", "SDMM", "SDM", "SDDD", "SDD", "SDW",
		"STHH", "STH", "STMM", "STM", "STSS", "STS",
		"EDYYYY", "EDYY", "EDMM", "EDM", "EDDD", "EDD", "EDW",
		"ETHH", "ETH", "ETMM", "ETM", "ETSS", "ETS",
		"DUHH", "DUH", "DUMM", "DUM", "DUSS", "DUS",
		"ONID10", "TSID10", "SID10", "EID10", "ONID16", "TSID16", "SID16", "EID16",
		"ReserveID":
		return true
	default:
		return false
	}
}

// expandOutputNameは予約の保存値だけを使い、上限内の一つのTSファイル名を作る。
func expandOutputName(reservation Reservation) (string, error) {
	if reservation.Output.Validate() != nil || reservation.Number < 1 || reservation.Program.Start.IsZero() ||
		reservation.Program.Start.Location() != time.UTC || reservation.Program.Duration < time.Second ||
		reservation.Program.Duration > 24*time.Hour || reservation.Program.Duration%time.Second != 0 {
		return "", errors.New("recording: invalid output expansion source")
	}
	template := reservation.Output.Template
	var result strings.Builder
	for offset := 0; offset < len(template); {
		start := strings.IndexByte(template[offset:], '$')
		if start < 0 {
			result.WriteString(template[offset:])
			break
		}
		start += offset
		result.WriteString(template[offset:start])
		end := strings.IndexByte(template[start+1:], '$')
		if end < 0 {
			return "", errors.New("recording: invalid output template")
		}
		end += start + 1
		value, ok := outputMacroValue(template[start+1:end], reservation)
		if !ok {
			return "", errors.New("recording: unsupported output macro")
		}
		result.WriteString(value)
		offset = end + 1
	}
	name := strings.TrimSpace(result.String())
	name = strings.TrimRight(name, ".")
	if name == "" || name == "." || name == ".." {
		return "", errors.New("recording: empty expanded output name")
	}
	if !strings.EqualFold(path.Ext(name), ".ts") {
		name += ".ts"
	}
	if !validOutputName(name) {
		return "", errors.New("recording: invalid expanded output name")
	}
	return name, nil
}

// outputMacroValueはEDCB互換マクロのうち外部状態を必要としない固定範囲を展開する。
func outputMacroValue(name string, reservation Reservation) (string, bool) {
	program := reservation.Program
	start := program.Start.In(japanStandardTime)
	end := program.Start.Add(program.Duration).In(japanStandardTime)
	switch name {
	case "Title":
		return sanitizeOutputValue(program.Title), true
	case "Title2":
		return sanitizeOutputValue(withoutSquareBrackets(program.Title)), true
	case "ServiceName":
		return sanitizeOutputValue(program.StationName), true
	case "ONID10":
		return strconv.FormatUint(uint64(program.NetworkID), 10), true
	case "TSID10":
		return strconv.FormatUint(uint64(program.TransportStreamID), 10), true
	case "SID10":
		return strconv.FormatUint(uint64(program.ServiceID), 10), true
	case "EID10":
		return strconv.FormatUint(uint64(program.EventID), 10), true
	case "ONID16":
		return fmt.Sprintf("%04X", program.NetworkID), true
	case "TSID16":
		return fmt.Sprintf("%04X", program.TransportStreamID), true
	case "SID16":
		return fmt.Sprintf("%04X", program.ServiceID), true
	case "EID16":
		return fmt.Sprintf("%04X", program.EventID), true
	case "ReserveID":
		return strconv.FormatInt(int64(reservation.Number), 10), true
	case "DUHH":
		return fmt.Sprintf("%02d", int64(program.Duration/time.Hour)), true
	case "DUH":
		return strconv.FormatInt(int64(program.Duration/time.Hour), 10), true
	case "DUMM":
		return fmt.Sprintf("%02d", int64(program.Duration/time.Minute)%60), true
	case "DUM":
		return strconv.FormatInt(int64(program.Duration/time.Minute)%60, 10), true
	case "DUSS":
		return fmt.Sprintf("%02d", int64(program.Duration/time.Second)%60), true
	case "DUS":
		return strconv.FormatInt(int64(program.Duration/time.Second)%60, 10), true
	}
	if strings.HasPrefix(name, "S") {
		return timeMacroValue(name[1:], start)
	}
	if strings.HasPrefix(name, "E") {
		return timeMacroValue(name[1:], end)
	}
	return "", false
}

func timeMacroValue(name string, value time.Time) (string, bool) {
	switch name {
	case "DYYYY":
		return fmt.Sprintf("%04d", value.Year()), true
	case "DYY":
		return fmt.Sprintf("%02d", value.Year()%100), true
	case "DMM":
		return fmt.Sprintf("%02d", int(value.Month())), true
	case "DM":
		return strconv.Itoa(int(value.Month())), true
	case "DDD":
		return fmt.Sprintf("%02d", value.Day()), true
	case "DD":
		return strconv.Itoa(value.Day()), true
	case "DW":
		return []string{"日", "月", "火", "水", "木", "金", "土"}[value.Weekday()], true
	case "THH":
		return fmt.Sprintf("%02d", value.Hour()), true
	case "TH":
		return strconv.Itoa(value.Hour()), true
	case "TMM":
		return fmt.Sprintf("%02d", value.Minute()), true
	case "TM":
		return strconv.Itoa(value.Minute()), true
	case "TSS":
		return fmt.Sprintf("%02d", value.Second()), true
	case "TS":
		return strconv.Itoa(value.Second()), true
	default:
		return "", false
	}
}

// withoutSquareBracketsはTitle2用に対になった角括弧と中身だけを除く。
func withoutSquareBrackets(value string) string {
	var result strings.Builder
	for offset := 0; offset < len(value); {
		start := strings.IndexByte(value[offset:], '[')
		if start < 0 {
			result.WriteString(value[offset:])
			break
		}
		start += offset
		end := strings.IndexByte(value[start+1:], ']')
		if end < 0 {
			result.WriteString(value[offset:])
			break
		}
		result.WriteString(value[offset:start])
		offset = start + 1 + end + 1
	}
	return result.String()
}

// sanitizeOutputValueは番組名と放送局名を単一の移植可能なファイル名要素へ変換する。
func sanitizeOutputValue(value string) string {
	return strings.Map(func(char rune) rune {
		if char == '\r' || char == '\n' {
			return -1
		}
		if char < 0x20 || char == 0x7f || strings.ContainsRune(`/\\<>:"|?*`, char) {
			return '_'
		}
		return char
	}, value)
}

func validOutputName(value string) bool {
	return utf8.ValidString(value) && len(value) >= 1 && len(value) <= MaxOutputNameBytes && value != "." &&
		value != ".." && !strings.ContainsAny(value, "/\\") && !containsControl(value)
}

func containsControl(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}
