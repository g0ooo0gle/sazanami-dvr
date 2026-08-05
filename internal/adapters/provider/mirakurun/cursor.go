package mirakurun

import (
	"context"
	"sync"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/catalog"
)

type serviceCursor struct {
	mu         sync.Mutex
	operation  *responseOperation
	limit      int
	total      int
	closed     bool
	provenance provider.Provenance
}

// Nextはservice配列を最大request limit件だけdecodeし、配列全体を保持しない。
func (cursor *serviceCursor) Next(ctx context.Context) (catalog.ServicePage, error) {
	cursor.mu.Lock()
	defer cursor.mu.Unlock()
	if cursor.closed {
		return catalog.ServicePage{}, provider.NewFailure(provider.ReasonRejected, "service-cursor-closed")
	}
	if err := provider.ContextFailure(ctx); err != nil {
		cursor.closed = true
		_ = cursor.operation.close()
		return catalog.ServicePage{}, err
	}
	page := catalog.ServicePage{Items: make([]catalog.ServiceObservation, 0, cursor.limit)}
	for len(page.Items) < cursor.limit && cursor.operation.decoder.More() {
		if cursor.total >= provider.MaxServiceOperation {
			cursor.closed = true
			return catalog.ServicePage{}, cursor.operation.failure(provider.NewFailure(provider.ReasonOverLimit, "service-count-over-limit"))
		}
		item, err := decodeService(cursor.operation.decoder, cursor.provenance)
		if err != nil {
			cursor.closed = true
			return catalog.ServicePage{}, cursor.operation.failure(err)
		}
		page.Items = append(page.Items, item)
		cursor.total++
	}
	if len(page.Items) < cursor.limit {
		if err := cursor.operation.finishArray(); err != nil {
			cursor.closed = true
			return catalog.ServicePage{}, cursor.operation.failure(err)
		}
		page.End = true
		cursor.closed = true
		if err := cursor.operation.close(); err != nil {
			return catalog.ServicePage{}, err
		}
	}
	return page, nil
}

// Closeはservice responseをidempotentに閉じ、adapterの逐次実行権を解放する。
func (cursor *serviceCursor) Close() error {
	cursor.mu.Lock()
	defer cursor.mu.Unlock()
	cursor.closed = true
	return cursor.operation.close()
}

type programCursor struct {
	mu         sync.Mutex
	operation  *responseOperation
	limit      int
	total      int
	closed     bool
	provenance provider.Provenance
}

// Nextは番組配列を最大request limit件だけdecodeし、配列全体を保持しない。
func (cursor *programCursor) Next(ctx context.Context) (catalog.ProgramPage, error) {
	cursor.mu.Lock()
	defer cursor.mu.Unlock()
	if cursor.closed {
		return catalog.ProgramPage{}, provider.NewFailure(provider.ReasonRejected, "program-cursor-closed")
	}
	if err := provider.ContextFailure(ctx); err != nil {
		cursor.closed = true
		_ = cursor.operation.close()
		return catalog.ProgramPage{}, err
	}
	page := catalog.ProgramPage{Items: make([]catalog.ProgramObservation, 0, cursor.limit)}
	for len(page.Items) < cursor.limit && cursor.operation.decoder.More() {
		if cursor.total >= provider.MaxProgramOperation {
			cursor.closed = true
			return catalog.ProgramPage{}, cursor.operation.failure(provider.NewFailure(provider.ReasonOverLimit, "program-count-over-limit"))
		}
		item, err := decodeProgram(cursor.operation.decoder, cursor.provenance)
		if err != nil {
			cursor.closed = true
			return catalog.ProgramPage{}, cursor.operation.failure(err)
		}
		page.Items = append(page.Items, item)
		cursor.total++
	}
	if len(page.Items) < cursor.limit {
		if err := cursor.operation.finishArray(); err != nil {
			cursor.closed = true
			return catalog.ProgramPage{}, cursor.operation.failure(err)
		}
		page.End = true
		cursor.closed = true
		if err := cursor.operation.close(); err != nil {
			return catalog.ProgramPage{}, err
		}
	}
	return page, nil
}

// Closeはprogram responseをidempotentに閉じ、adapterの逐次実行権を解放する。
func (cursor *programCursor) Close() error {
	cursor.mu.Lock()
	defer cursor.mu.Unlock()
	cursor.closed = true
	return cursor.operation.close()
}

var _ catalog.ServiceCursor = (*serviceCursor)(nil)
var _ catalog.ProgramCursor = (*programCursor)(nil)
