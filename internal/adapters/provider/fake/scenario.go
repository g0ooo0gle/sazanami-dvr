package fake

import (
	"fmt"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/catalog"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/health"
)

// StreamStepKindはFake streamが次のReadで実行する動作を表す。
type StreamStepKind string

const (
	StepChunk    StreamStepKind = "CHUNK"
	StepStall    StreamStepKind = "STALL"
	StepCleanEnd StreamStepKind = "CLEAN_END"
	StepEarlyEOF StreamStepKind = "EARLY_EOF"
	StepFailure  StreamStepKind = "FAILURE"
)

// StreamStepはchunk、stall、終端、errorのいずれか1 stepを定義する。
type StreamStep struct {
	Kind       StreamStepKind
	Data       []byte
	ReleaseAt  int64
	Reason     provider.Reason
	Diagnostic string
}

// FaultPointはcatalog、stream、healthのどこで故障を注入するかを表す。
type FaultPoint string

const (
	FaultServiceBefore FaultPoint = "SERVICE_BEFORE_PAGE"
	FaultServiceAfter  FaultPoint = "SERVICE_AFTER_PAGE"
	FaultProgramBefore FaultPoint = "PROGRAM_BEFORE_PAGE"
	FaultProgramAfter  FaultPoint = "PROGRAM_AFTER_PAGE"
	FaultStreamOpen    FaultPoint = "STREAM_OPEN"
	FaultHealth        FaultPoint = "HEALTH"
)

// Faultは指定pointとindexで返す分類済みerrorを定義する。
type Fault struct {
	Point      FaultPoint
	Index      int
	Reason     provider.Reason
	Diagnostic string
}

// ExpectedRequestsはFakeが受理するrequest条件を必要な項目だけ固定する。
type ExpectedRequests struct {
	ServiceLimit      int
	ProgramLimit      int
	StreamTarget      string
	HealthCorrelation string
}

// ExpectedCountsはScenario完了後に照合するPort呼出回数を定義する。
type ExpectedCounts struct {
	ServiceOpens int
	ProgramOpens int
	StreamOpens  int
	HealthCalls  int
}

// Configは外部I/Oなしで再現するScenario全体を記述する。
type Config struct {
	ID             string
	Seed           uint64
	WallTime       time.Time
	InitialNanos   int64
	ServicePages   []catalog.ServicePage
	ProgramPages   []catalog.ProgramPage
	StreamSteps    []StreamStep
	Health         health.Observation
	Faults         []Fault
	Expected       ExpectedRequests
	ExpectedCounts ExpectedCounts
	HistoryLimit   int
}

// Scenarioは生成後に変更できない。参照用methodはdefensive copyを返す。
type Scenario struct {
	id             string
	seed           uint64
	wallTime       time.Time
	initialNanos   int64
	servicePages   []catalog.ServicePage
	programPages   []catalog.ProgramPage
	streamSteps    []StreamStep
	health         health.Observation
	faults         []Fault
	expected       ExpectedRequests
	expectedCounts ExpectedCounts
	historyLimit   int
}

// NewScenarioは全size・文字列・stepを検証し、入力とmemoryを共有しないScenarioを生成する。
func NewScenario(config Config) (*Scenario, error) {
	if err := validateText(config.ID, provider.MaxDiagnosticBytes, "scenario-id"); err != nil {
		return nil, err
	}
	if config.WallTime.IsZero() {
		return nil, malformed("missing-wall-time")
	}
	if config.InitialNanos < 0 {
		return nil, malformed("negative-monotonic-time")
	}
	if len(config.ServicePages)+len(config.ProgramPages) > provider.MaxScenarioPages {
		return nil, overLimit("scenario-pages")
	}
	serviceTotal := 0
	servicePages := make([]catalog.ServicePage, len(config.ServicePages))
	for i, page := range config.ServicePages {
		if len(page.Items) > provider.MaxCatalogPage {
			return nil, overLimit("service-page")
		}
		serviceTotal += len(page.Items)
		if serviceTotal > provider.MaxServiceOperation {
			return nil, overLimit("service-operation")
		}
		for _, item := range page.Items {
			if err := validateService(item); err != nil {
				return nil, fmt.Errorf("service page %d: %w", i, err)
			}
		}
		servicePages[i] = catalog.CloneServicePage(page)
	}
	programTotal := 0
	programPages := make([]catalog.ProgramPage, len(config.ProgramPages))
	for i, page := range config.ProgramPages {
		if len(page.Items) > provider.MaxCatalogPage {
			return nil, overLimit("program-page")
		}
		programTotal += len(page.Items)
		if programTotal > provider.MaxProgramOperation {
			return nil, overLimit("program-operation")
		}
		for _, item := range page.Items {
			if err := validateProgram(item); err != nil {
				return nil, fmt.Errorf("program page %d: %w", i, err)
			}
		}
		programPages[i] = catalog.CloneProgramPage(page)
	}

	chunkCount := 0
	nonChunkCount := 0
	streamSteps := make([]StreamStep, len(config.StreamSteps))
	for i, step := range config.StreamSteps {
		if err := validateText(step.Diagnostic, provider.MaxDiagnosticBytes, "stream-diagnostic"); err != nil && step.Diagnostic != "" {
			return nil, err
		}
		switch step.Kind {
		case StepChunk:
			chunkCount++
			if len(step.Data) == 0 || len(step.Data) > provider.MaxStreamChunk {
				return nil, overLimit("stream-chunk")
			}
			step.Data = append([]byte(nil), step.Data...)
		case StepStall:
			nonChunkCount++
			if step.ReleaseAt < 0 {
				return nil, malformed("negative-stall-time")
			}
		case StepCleanEnd, StepEarlyEOF, StepFailure:
			nonChunkCount++
		default:
			return nil, malformed("unknown-stream-step")
		}
		streamSteps[i] = step
	}
	if chunkCount > provider.MaxScenarioChunks {
		return nil, overLimit("scenario-chunks")
	}
	if nonChunkCount > provider.MaxScenarioFaults {
		return nil, overLimit("scenario-terminal-steps")
	}
	if len(config.Faults) > provider.MaxScenarioFaults {
		return nil, overLimit("scenario-faults")
	}
	for name, value := range map[string]string{
		"expected-stream-target":      config.Expected.StreamTarget,
		"expected-health-correlation": config.Expected.HealthCorrelation,
	} {
		if err := validateText(value, provider.MaxDiagnosticBytes, name); err != nil {
			return nil, err
		}
	}
	for name, value := range map[string]int{
		"expected-service-opens": config.ExpectedCounts.ServiceOpens,
		"expected-program-opens": config.ExpectedCounts.ProgramOpens,
		"expected-stream-opens":  config.ExpectedCounts.StreamOpens,
		"expected-health-calls":  config.ExpectedCounts.HealthCalls,
	} {
		if value < 0 || value > provider.MaxHistoryEvents {
			return nil, overLimit(name)
		}
	}
	for name, value := range map[string]int{
		"expected-service-limit": config.Expected.ServiceLimit,
		"expected-program-limit": config.Expected.ProgramLimit,
	} {
		if value < 0 || value > provider.MaxCatalogPage {
			return nil, overLimit(name)
		}
	}
	faults := make([]Fault, len(config.Faults))
	copy(faults, config.Faults)
	for _, fault := range faults {
		if fault.Index < 0 {
			return nil, malformed("negative-fault-index")
		}
		if err := validateText(fault.Diagnostic, provider.MaxDiagnosticBytes, "fault-diagnostic"); err != nil && fault.Diagnostic != "" {
			return nil, err
		}
	}
	if err := validateHealth(config.Health); err != nil {
		return nil, err
	}
	historyLimit := config.HistoryLimit
	if historyLimit == 0 {
		historyLimit = provider.MaxHistoryEvents
	}
	if historyLimit < 1 || historyLimit > provider.MaxHistoryEvents {
		return nil, overLimit("history-limit")
	}
	return &Scenario{
		id:             config.ID,
		seed:           config.Seed,
		wallTime:       config.WallTime.UTC(),
		initialNanos:   config.InitialNanos,
		servicePages:   servicePages,
		programPages:   programPages,
		streamSteps:    streamSteps,
		health:         health.CloneObservation(config.Health),
		faults:         faults,
		expected:       config.Expected,
		expectedCounts: config.ExpectedCounts,
		historyLimit:   historyLimit,
	}, nil
}

func validateService(value catalog.ServiceObservation) error {
	for name, text := range map[string]string{
		"service-locator": value.Locator,
		"broadcast":       value.Broadcast,
		"display-name":    value.DisplayName,
	} {
		if err := validateText(text, provider.MaxDiagnosticBytes, name); err != nil {
			return err
		}
	}
	return nil
}

func validateProgram(value catalog.ProgramObservation) error {
	for name, text := range map[string]string{
		"service-locator": value.ServiceLocator,
		"event-locator":   value.EventLocator,
		"title":           value.Title,
		"description":     value.Description,
		"revision":        value.RevisionFingerprint,
	} {
		if err := validateText(text, provider.MaxDiagnosticBytes, name); err != nil {
			return err
		}
	}
	return nil
}

func validateHealth(value health.Observation) error {
	for name, text := range map[string]string{
		"health-version":             value.Version,
		"health-tuner-summary":       value.TunerSummary,
		"health-provenance-backend":  value.Provenance.Backend,
		"health-provenance-revision": value.Provenance.Revision,
	} {
		if err := validateText(text, provider.MaxDiagnosticBytes, name); err != nil {
			return err
		}
	}
	size := len(value.Version) + len(value.TunerSummary) + len(value.Provenance.Backend) + len(value.Provenance.Revision)
	for _, capability := range value.Capabilities {
		size += len(capability)
	}
	if size > provider.MaxHealthEnvelope {
		return overLimit("health-envelope")
	}
	return nil
}

func validateText(value string, limit int, field string) error {
	if value == "" {
		if field == "scenario-id" {
			return malformed("empty-scenario-id")
		}
		return nil
	}
	if len(value) > limit {
		return overLimit(field)
	}
	return nil
}

func (s *Scenario) servicePage(index int) (catalog.ServicePage, bool) {
	if index >= len(s.servicePages) {
		return catalog.ServicePage{End: true}, false
	}
	return catalog.CloneServicePage(s.servicePages[index]), true
}

func (s *Scenario) programPage(index int) (catalog.ProgramPage, bool) {
	if index >= len(s.programPages) {
		return catalog.ProgramPage{End: true}, false
	}
	return catalog.CloneProgramPage(s.programPages[index]), true
}

func (s *Scenario) streamStep(index int) (StreamStep, bool) {
	if index >= len(s.streamSteps) {
		return StreamStep{Kind: StepCleanEnd}, false
	}
	step := s.streamSteps[index]
	step.Data = append([]byte(nil), step.Data...)
	return step, true
}

func (s *Scenario) fault(point FaultPoint, index int) *provider.Failure {
	for _, fault := range s.faults {
		if fault.Point == point && fault.Index == index {
			return provider.NewFailure(fault.Reason, fault.Diagnostic)
		}
	}
	return nil
}

func malformed(reason string) error     { return provider.NewFailure(provider.ReasonMalformed, reason) }
func overLimit(reason string) error     { return provider.NewFailure(provider.ReasonOverLimit, reason) }
func providerError(reason string) error { return provider.NewFailure(provider.ReasonInternal, reason) }
