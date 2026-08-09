package runtime

import (
	"context"
	"sync/atomic"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

// SnapshotLoaderはCtrlCmd要求の開始時に、現在公開されているスナップショットを一度だけ返す。
type SnapshotLoader interface {
	Load() *Snapshot
}

// SnapshotHolderは検証済みスナップショット一つをatomicに公開する。
// Loadで得た値は不変であり、呼び出し側は一操作が終わるまで保持してよい。
type SnapshotHolder struct {
	current atomic.Pointer[Snapshot]
}

// NewSnapshotHolderは利用可能な初期スナップショットを持つ保持器を作る。
func NewSnapshotHolder(initial *Snapshot) (*SnapshotHolder, error) {
	if initial == nil {
		return nil, stable("channel-snapshot-failed")
	}
	holder := &SnapshotHolder{}
	holder.current.Store(initial)
	return holder, nil
}

// Loadは現在公開されている不変スナップショットを返す。
func (holder *SnapshotHolder) Load() *Snapshot {
	if holder == nil {
		return nil
	}
	return holder.current.Load()
}

// Storeは完了済み世代から作った新しいスナップショットへ一度に切り替える。
func (holder *SnapshotHolder) Store(next *Snapshot) error {
	if holder == nil || next == nil {
		return stable("channel-snapshot-failed")
	}
	holder.current.Store(next)
	return nil
}

// FindProgramは予約操作の開始時点に公開されている一世代だけで番組を照合する。
func (holder *SnapshotHolder) FindProgram(ctx context.Context, request recording.ReservationRequest) (recording.ProgramSnapshot, error) {
	snapshot := holder.Load()
	if snapshot == nil {
		return recording.ProgramSnapshot{}, stable("program-not-reservable")
	}
	return snapshot.FindProgram(ctx, request)
}
