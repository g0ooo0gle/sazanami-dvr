package catalogmodel

import "context"

const (
	// MaxWriteBatchは1回のcatalog write transactionへ含められる観測数である。
	MaxWriteBatch = 100
	// MaxQueryPageは1回のcatalog queryが返せる最大件数である。
	MaxQueryPage = 256
)

// Backendはcredentialやendpointを含まないprovider instanceの永続化入力である。
type Backend struct {
	ID              ID
	Kind            string
	IdentityHash    [32]byte
	ReportedVersion *string
	SourceRef       *string
	ObservedAtMS    int64
}

// Syncは1回のbounded catalog収集を表す。
type Sync struct {
	ID                  ID
	BackendID           ID
	StartedAtMS         int64
	CorrelationID       string
	VerifiedFakeLineage bool
}

// GenerationStateは番組表世代を事前検証する段階と、利用者へ公開できる段階を区別する。
type GenerationState uint8

const (
	// GenerationRunningは取得中または完了直前の検証中で、まだ公開してはいけない世代である。
	GenerationRunning GenerationState = iota + 1
	// GenerationCompletedは取得と事前検証を終え、固定読み取りに利用できる世代である。
	GenerationCompleted
)

// ServiceObservationはprovider表現を正規化したservice観測である。
type ServiceObservation struct {
	ProviderLocator string
	NetworkID       *int64
	TransportID     *int64
	ServiceID       *int64
	BroadcastKind   *string
	DisplayName     string
	TuningTarget    *string
	Validation      Validation
	Reason          *string
}

// ProgramObservationは1つのserviceに属する番組観測とcanonical materialである。
type ProgramObservation struct {
	ServiceLocator string
	EventLocator   string
	RawEventID     *int64
	Material       RevisionMaterial
	Reason         *string
}

// CurrentProgramは最後に完了したgenerationから返すbounded read modelである。
type CurrentProgram struct {
	InstanceID     ID
	RevisionID     ID
	ServiceLocator string
	EventLocator   string
	RawEventID     *int64
	RevisionNumber int64
	Hash           [32]byte
	Material       RevisionMaterial
	Classification Classification
}

// ProgramCursorは番組を放送サービスとprovider内eventの順に読み進める位置である。
type ProgramCursor struct {
	ServiceLocator string
	EventLocator   string
}

// CurrentBackendは少なくとも1つのCOMPLETED generationを持つbackendのbounded read modelである。
type CurrentBackend struct {
	ID              ID
	Kind            string
	ReportedVersion *string
	LastSeenAtMS    int64
}

// CurrentServiceは最後に完了したgenerationから返すservice表示用のbounded read modelである。
type CurrentService struct {
	ID              ID
	ProviderLocator string
	DisplayName     string
	NetworkID       *int64
	TransportID     *int64
	ServiceID       *int64
	BroadcastKind   *string
	Validation      Validation
}

// CatalogReaderは運用表示が利用するcompleted catalogのread-only Portである。
type CatalogReader interface {
	CurrentBackends(context.Context, int, ID) ([]CurrentBackend, error)
	CurrentServices(context.Context, ID, int, ID) ([]CurrentService, error)
	CurrentProgramsInWindow(context.Context, ID, int64, int64, int) ([]CurrentProgram, bool, error)
}

// Repositoryはcatalog transactionの永続化Portであり、SQLやdriver typeを公開しない。
type Repository interface {
	EnsureBackend(context.Context, Backend) error
	BeginSync(context.Context, Sync) error
	StoreServices(context.Context, ID, []ServiceObservation) error
	StorePrograms(context.Context, ID, bool, []ProgramObservation) error
	CompleteSync(context.Context, ID, int64, int, int) error
	FailSync(context.Context, ID, int64, string) error
	CurrentPrograms(context.Context, ID, int, ID) ([]CurrentProgram, error)
}

// SyncRecoveryはprocess終了で残ったRUNNING generationを起動前に閉じるPortである。
type SyncRecovery interface {
	ReconcileRunningSyncs(context.Context, int64) (int, error)
}
