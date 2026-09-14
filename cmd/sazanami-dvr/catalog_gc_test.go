package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/g0ooo0gle/sazanami-dvr/internal/app/cataloggc"
)

func TestCatalogGCOutputIsSeparateAndRedacted(t *testing.T) {
	var output, diagnostic bytes.Buffer
	observe := observeCatalogGC(&output, &diagnostic)
	observe(cataloggc.Result{
		ProgramObservationsDeleted: 1, ServiceObservationsDeleted: 2, CatalogSyncsDeleted: 3,
		ProgramRevisionsDeleted: 4, ProgramInstancesDeleted: 5, ServicesDeleted: 6,
		Completed: true, DurationMS: 7,
	}, nil)
	observe(cataloggc.Result{More: true, DurationMS: 8}, nil)
	observe(cataloggc.Result{DurationMS: 9}, errors.New("raw sqlite path /private/catalog.sqlite3"))

	wantOutput := "catalog_gc program_observations_deleted=1 service_observations_deleted=2 catalog_syncs_deleted=3 program_revisions_deleted=4 program_instances_deleted=5 services_deleted=6 more=false duration_ms=7 reason=completed\n" +
		"catalog_gc program_observations_deleted=0 service_observations_deleted=0 catalog_syncs_deleted=0 program_revisions_deleted=0 program_instances_deleted=0 services_deleted=0 more=true duration_ms=8 reason=deadline\n"
	wantDiagnostic := "catalog_gc program_observations_deleted=0 service_observations_deleted=0 catalog_syncs_deleted=0 program_revisions_deleted=0 program_instances_deleted=0 services_deleted=0 more=false duration_ms=9 reason=catalog-gc-failed\n"
	if output.String() != wantOutput || diagnostic.String() != wantDiagnostic {
		t.Fatalf("output=%q diagnostic=%q", output.String(), diagnostic.String())
	}
	for _, private := range []string{"http://", "/private/", "raw sqlite", "catalog.sqlite3", "番組"} {
		if strings.Contains(output.String(), private) || strings.Contains(diagnostic.String(), private) {
			t.Fatalf("private value=%q", private)
		}
	}
}
