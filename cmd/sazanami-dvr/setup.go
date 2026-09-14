package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"time"

	ctrlcmdruntime "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/runtime"
	mirakurunadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/provider/mirakurun"
	sqliteadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/sqlite"
	"github.com/g0ooo0gle/sazanami-dvr/internal/app/catalogsync"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

const (
	defaultSetupDataRoot = "/var/lib/sazanami-dvr"
	setupTimeout         = 30 * time.Minute
)

var errSetupUsage = errors.New("setup usage")

func runSetupCommand(parent context.Context, arguments []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	mirakurunURL := flags.String("mirakurun-url", "", "Mirakurunのoperator設定URL")
	dataRoot := flags.String("data-root", defaultSetupDataRoot, "owner-onlyのデータディレクトリ")
	if err := flags.Parse(arguments); err != nil || *mirakurunURL == "" || flags.NArg() != 0 {
		return errSetupUsage
	}
	adapter, err := mirakurunadapter.New(*mirakurunURL)
	if err != nil {
		return errorsStable("provider-configuration-invalid")
	}
	defer adapter.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(parent, setupTimeout)
	defer cancel()
	store, _, err := sqliteadapter.OpenStoreForSetup(ctx, *dataRoot, time.Now().UTC())
	if err != nil {
		if errors.Is(err, sqliteadapter.ErrSetupDatabaseNotCurrent) {
			return errorsStable("current-database-required")
		}
		return errorsStable("database-owner-unavailable")
	}
	defer store.Close()
	clock := wallClock{}
	if _, err := (catalogsync.RecoveryService{Repository: store, Clock: clock}).Reconcile(ctx); err != nil {
		return errorsStable("startup-recovery-failed")
	}
	_, _ = newCatalogGC(store, clock)(ctx)
	if ctx.Err() != nil {
		return errorsStable("setup-canceled")
	}

	backendID, err := syncSetupCatalog(ctx, adapter, store, clock)
	if err != nil {
		return err
	}
	services, err := adapter.ObserveBootstrapServices(ctx)
	if err != nil {
		return errorsStable("channel-service-discovery-failed")
	}
	services = selectedBootstrapServices(services)
	if len(services) == 0 {
		return errorsStable("channel-service-unavailable")
	}

	stream, err := mirakurunadapter.NewStream(*mirakurunURL)
	if err != nil {
		return errorsStable("provider-configuration-invalid")
	}
	defer stream.CloseIdleConnections()
	generated := make([]ctrlcmdruntime.GeneratedService, 0, len(services))
	for _, service := range services {
		transportStreamID, probeErr := stream.ProbeTransportStreamID(ctx, service.Locator, service.ServiceID)
		if probeErr != nil {
			return errorsStable("channel-pat-probe-failed")
		}
		generated = append(generated, ctrlcmdruntime.GeneratedService{
			ProviderLocator: service.Locator, NetworkID: service.NetworkID, ServiceID: service.ServiceID,
			TransportStreamID: transportStreamID, RemoteControlKey: service.RemoteControlKey,
		})
	}
	result, err := ctrlcmdruntime.PublishGeneratedChannelMap(ctx, *dataRoot, backendID, generated, store)
	if err != nil {
		return stableSetupPublishError(err)
	}
	fmt.Fprintf(stdout, "setup result=completed services=%d channel_map=%s\n", result.ServiceCount, result.Status)
	return nil
}

func syncSetupCatalog(ctx context.Context, adapter *mirakurunadapter.Adapter, store *sqliteadapter.Store,
	clock wallClock,
) (catalogmodel.ID, error) {
	var reportedVersion *string
	if observed, err := adapter.ObserveVersion(ctx); err == nil {
		reportedVersion = &observed.Current
	}
	identityHash := adapter.IdentityHash()
	backendID := stableBackendID(identityHash)
	correlationID, err := catalogmodel.NewID()
	if err != nil {
		return catalogmodel.ID{}, errorsStable("correlation-id-generation-failed")
	}
	sourceRef := "mirakurun-http-json-v1"
	_, err = (catalogsync.Service{Provider: adapter, Repository: store, Clock: clock}).Sync(ctx, catalogsync.Request{
		Backend: catalogmodel.Backend{
			ID: backendID, Kind: "MIRAKURUN", IdentityHash: identityHash,
			ReportedVersion: reportedVersion, SourceRef: &sourceRef,
		},
		CorrelationID: correlationID.String(), ServicePageLimit: 256, ProgramPageLimit: 256,
		VerifiedFakeLineage: false,
	})
	if err != nil {
		return catalogmodel.ID{}, errorsStable("catalog-sync-failed")
	}
	return backendID, nil
}

func selectedBootstrapServices(services []mirakurunadapter.BootstrapService) []mirakurunadapter.BootstrapService {
	selected := services[:0]
	for _, service := range services {
		switch service.ServiceType {
		case 0x01, 0x02, 0xa1, 0xa2:
			selected = append(selected, service)
		}
	}
	sort.Slice(selected, func(left, right int) bool {
		if selected[left].NetworkID != selected[right].NetworkID {
			return selected[left].NetworkID < selected[right].NetworkID
		}
		if selected[left].ServiceID != selected[right].ServiceID {
			return selected[left].ServiceID < selected[right].ServiceID
		}
		return selected[left].Locator < selected[right].Locator
	})
	return selected
}

func stableSetupPublishError(err error) error {
	switch err.Error() {
	case "channel-map-exists", "channel-map-not-regular":
		return errorsStable(err.Error())
	case "channel-context-ended":
		return errorsStable("setup-canceled")
	default:
		return errorsStable("channel-map-publish-failed")
	}
}
