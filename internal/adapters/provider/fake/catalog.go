package fake

import (
	"context"
	"sync"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/catalog"
)

// CatalogProviderはScenarioのserviceとprogramをpull型cursorで返すFake Portである。
type CatalogProvider struct{ runtime *runtimeState }

// OpenServicesはrequestを検証し、同時open上限内でservice cursorを生成する。
func (p *CatalogProvider) OpenServices(ctx context.Context, request catalog.ServiceRequest) (catalog.ServiceCursor, error) {
	if err := provider.ContextFailure(ctx); err != nil {
		return nil, err
	}
	if err := validateText(request.CorrelationID, provider.MaxDiagnosticBytes, "service-correlation"); err != nil || request.CorrelationID == "" {
		if err != nil {
			return nil, err
		}
		return nil, malformed("empty-service-correlation")
	}
	limit, err := provider.EffectiveLimit(request.Limit, provider.MaxCatalogPage)
	if err != nil {
		return nil, err
	}
	if expected := p.runtime.scenario.expected.ServiceLimit; expected != 0 && expected != limit {
		return nil, provider.NewFailure(provider.ReasonRejected, "service-request-mismatch")
	}
	p.runtime.mu.Lock()
	defer p.runtime.mu.Unlock()
	if err := p.runtime.acquireLocked("service-open"); err != nil {
		return nil, err
	}
	p.runtime.counters.ServiceOpens++
	return &serviceCursor{runtime: p.runtime, limit: limit}, nil
}

// OpenProgramsはrequestを検証し、同時open上限内でprogram cursorを生成する。
func (p *CatalogProvider) OpenPrograms(ctx context.Context, request catalog.ProgramRequest) (catalog.ProgramCursor, error) {
	if err := provider.ContextFailure(ctx); err != nil {
		return nil, err
	}
	if err := validateText(request.CorrelationID, provider.MaxDiagnosticBytes, "program-correlation"); err != nil || request.CorrelationID == "" {
		if err != nil {
			return nil, err
		}
		return nil, malformed("empty-program-correlation")
	}
	limit, err := provider.EffectiveLimit(request.Limit, provider.MaxCatalogPage)
	if err != nil {
		return nil, err
	}
	if expected := p.runtime.scenario.expected.ProgramLimit; expected != 0 && expected != limit {
		return nil, provider.NewFailure(provider.ReasonRejected, "program-request-mismatch")
	}
	p.runtime.mu.Lock()
	defer p.runtime.mu.Unlock()
	if err := p.runtime.acquireLocked("program-open"); err != nil {
		return nil, err
	}
	p.runtime.counters.ProgramOpens++
	return &programCursor{runtime: p.runtime, limit: limit}, nil
}

type serviceCursor struct {
	mu      sync.Mutex
	runtime *runtimeState
	limit   int
	index   int
	total   int
	closed  bool
	pending error
}

// Nextは次のservice pageをdefensive copyで返し、faultと終端を再現する。
func (c *serviceCursor) Next(ctx context.Context) (catalog.ServicePage, error) {
	if err := provider.ContextFailure(ctx); err != nil {
		return catalog.ServicePage{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return catalog.ServicePage{}, provider.NewFailure(provider.ReasonRejected, "service-cursor-closed")
	}
	if c.pending != nil {
		err := c.pending
		c.pending = nil
		return catalog.ServicePage{}, err
	}
	if failure := c.runtime.scenario.fault(FaultServiceBefore, c.index); failure != nil {
		return catalog.ServicePage{}, failure
	}
	page, exists := c.runtime.scenario.servicePage(c.index)
	if len(page.Items) > c.limit {
		return catalog.ServicePage{}, overLimit("service-effective-page")
	}
	if c.total > provider.MaxServiceOperation-len(page.Items) {
		return catalog.ServicePage{}, overLimit("service-operation")
	}
	c.runtime.mu.Lock()
	if err := c.runtime.recordLocked("service-page", c.index); err != nil {
		c.runtime.mu.Unlock()
		return catalog.ServicePage{}, err
	}
	c.runtime.mu.Unlock()
	c.total += len(page.Items)
	current := c.index
	c.index++
	if failure := c.runtime.scenario.fault(FaultServiceAfter, current); failure != nil {
		c.pending = failure
	}
	if !exists {
		page.End = true
	}
	return catalog.CloneServicePage(page), nil
}

// Closeはservice cursorをidempotentに閉じ、共有runtimeのopen数を解放する。
func (c *serviceCursor) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	c.runtime.mu.Lock()
	defer c.runtime.mu.Unlock()
	c.runtime.counters.Closes++
	return c.runtime.releaseLocked("service-release")
}

type programCursor struct {
	mu      sync.Mutex
	runtime *runtimeState
	limit   int
	index   int
	total   int
	closed  bool
	pending error
}

// Nextは次のprogram pageをdefensive copyで返し、faultと終端を再現する。
func (c *programCursor) Next(ctx context.Context) (catalog.ProgramPage, error) {
	if err := provider.ContextFailure(ctx); err != nil {
		return catalog.ProgramPage{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return catalog.ProgramPage{}, provider.NewFailure(provider.ReasonRejected, "program-cursor-closed")
	}
	if c.pending != nil {
		err := c.pending
		c.pending = nil
		return catalog.ProgramPage{}, err
	}
	if failure := c.runtime.scenario.fault(FaultProgramBefore, c.index); failure != nil {
		return catalog.ProgramPage{}, failure
	}
	page, exists := c.runtime.scenario.programPage(c.index)
	if len(page.Items) > c.limit {
		return catalog.ProgramPage{}, overLimit("program-effective-page")
	}
	if c.total > provider.MaxProgramOperation-len(page.Items) {
		return catalog.ProgramPage{}, overLimit("program-operation")
	}
	c.runtime.mu.Lock()
	if err := c.runtime.recordLocked("program-page", c.index); err != nil {
		c.runtime.mu.Unlock()
		return catalog.ProgramPage{}, err
	}
	c.runtime.mu.Unlock()
	c.total += len(page.Items)
	current := c.index
	c.index++
	if failure := c.runtime.scenario.fault(FaultProgramAfter, current); failure != nil {
		c.pending = failure
	}
	if !exists {
		page.End = true
	}
	return catalog.CloneProgramPage(page), nil
}

// Closeはprogram cursorをidempotentに閉じ、共有runtimeのopen数を解放する。
func (c *programCursor) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	c.runtime.mu.Lock()
	defer c.runtime.mu.Unlock()
	c.runtime.counters.Closes++
	return c.runtime.releaseLocked("program-release")
}
