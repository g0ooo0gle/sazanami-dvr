// Package providerは外部I/Oの実装詳細を持たず、Provider Portを組み合わせる。
package provider

import (
	"context"

	coreprovider "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/catalog"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/health"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/stream"
)

const maxNoProgressReads = 64

// WalkRequestは境界確認で辿るcatalog上限とstream条件を指定する。
type WalkRequest struct {
	ServiceLimit int
	ProgramLimit int
	ReadStream   bool
	Stream       stream.Request
}

// WalkResultは各Portから確認できた件数とhealthをまとめる。
type WalkResult struct {
	Services    int
	Programs    int
	StreamBytes int64
	Health      health.Observation
}

// WalkerはCatalog、Stream、Healthの分離を保ったまま一連の境界確認を行う。
type Walker struct {
	Catalog catalog.CatalogProvider
	Stream  stream.Provider
	Health  health.Provider
}

// Walkは各cursorとleaseを必ず閉じ、page・read・無進行回数の上限内で処理する。
func (w Walker) Walk(ctx context.Context, request WalkRequest) (WalkResult, error) {
	var result WalkResult
	services, err := w.Catalog.OpenServices(ctx, catalog.ServiceRequest{
		CorrelationID: "walk-services",
		Limit:         request.ServiceLimit,
	})
	if err != nil {
		return result, err
	}
	defer services.Close()
	for pages := 0; pages <= coreprovider.MaxScenarioPages; pages++ {
		page, nextErr := services.Next(ctx)
		if nextErr != nil {
			return result, nextErr
		}
		result.Services += len(page.Items)
		if page.End {
			break
		}
		if pages == coreprovider.MaxScenarioPages {
			return result, coreprovider.NewFailure(coreprovider.ReasonOverLimit, "service-page-walk")
		}
	}

	programs, err := w.Catalog.OpenPrograms(ctx, catalog.ProgramRequest{
		CorrelationID: "walk-programs",
		Limit:         request.ProgramLimit,
	})
	if err != nil {
		return result, err
	}
	defer programs.Close()
	for pages := 0; pages <= coreprovider.MaxScenarioPages; pages++ {
		page, nextErr := programs.Next(ctx)
		if nextErr != nil {
			return result, nextErr
		}
		result.Programs += len(page.Items)
		if page.End {
			break
		}
		if pages == coreprovider.MaxScenarioPages {
			return result, coreprovider.NewFailure(coreprovider.ReasonOverLimit, "program-page-walk")
		}
	}

	if request.ReadStream {
		lease, openErr := w.Stream.OpenStream(ctx, request.Stream)
		if openErr != nil {
			return result, openErr
		}
		defer lease.Close()
		buffer := make([]byte, 32*1024)
		noProgress := 0
		// 誤実装されたProviderが無限にActiveを返しても、処理を成功扱いにしない。
		terminalReached := false
		for reads := 0; reads <= coreprovider.MaxScenarioChunks+coreprovider.MaxScenarioFaults; reads++ {
			n, terminal, readErr := lease.Read(ctx, buffer)
			if readErr != nil {
				return result, readErr
			}
			result.StreamBytes += int64(n)
			if terminal.Done {
				terminalReached = true
				break
			}
			if n == 0 {
				noProgress++
				if noProgress > maxNoProgressReads {
					return result, coreprovider.NewFailure(coreprovider.ReasonTimeout, "stream-no-progress")
				}
			} else {
				noProgress = 0
			}
		}
		if !terminalReached {
			return result, coreprovider.NewFailure(coreprovider.ReasonOverLimit, "stream-read-walk")
		}
	}

	result.Health, err = w.Health.Probe(ctx, health.Request{CorrelationID: "walk-health"})
	if err != nil {
		return result, err
	}
	return result, nil
}
