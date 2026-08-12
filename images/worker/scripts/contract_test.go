// Package scripts holds contract tests over the worker's bash Operation scripts.
// These tests read the shell scripts as text and assert on presence/absence of
// specific strings, guarding against regressions in the bash/Go split described
// in images/README.md.
package scripts

import (
	"os"
	"strings"
	"testing"
)

// TestAddRegionsImportsFullSpec asserts add-regions.sh no longer defers regions
// via NOMINATIM_IMPORT_MAX_REGIONS: it must import every region in
// NOMINATIM_REGIONS / Spec.Regions not already recorded in imported-regions.txt.
func TestAddRegionsImportsFullSpec(t *testing.T) {
	contents, err := os.ReadFile("add-regions.sh")
	if err != nil {
		t.Fatalf("reading add-regions.sh: %v", err)
	}
	script := string(contents)

	if strings.Contains(script, "NOMINATIM_IMPORT_MAX_REGIONS") {
		t.Error("add-regions.sh must not reference NOMINATIM_IMPORT_MAX_REGIONS; " +
			"operator chunks AddRegions and worker imports every desired region")
	}
	if strings.Contains(script, "deferring remaining regions") {
		t.Error("add-regions.sh must not defer remaining regions; " +
			"import every DESIRED_REGIONS entry not already imported")
	}
}

// TestAddRegionsBootstrapBelt uses require_bootstrap_ready (schema-aware last
// resort), not a hard die on missing IMPORT_FINISHED alone. Cluster SoT remains
// status.regions / Succeeded Bootstrap (nominatim-5et.35.2).
func TestAddRegionsBootstrapBelt(t *testing.T) {
	addRegions, err := os.ReadFile("add-regions.sh")
	if err != nil {
		t.Fatalf("reading add-regions.sh: %v", err)
	}
	if !strings.Contains(string(addRegions), "require_bootstrap_ready") {
		t.Error("add-regions.sh must call require_bootstrap_ready")
	}
	if !strings.Contains(string(addRegions), "NOMINATIM_REGIONS") {
		t.Error("add-regions.sh must still require NOMINATIM_REGIONS (regions belt)")
	}

	common, err := os.ReadFile("common.sh")
	if err != nil {
		t.Fatalf("reading common.sh: %v", err)
	}
	if !strings.Contains(string(common), "require_bootstrap_ready()") {
		t.Error("common.sh must define require_bootstrap_ready")
	}
	if !strings.Contains(string(common), "status.regions is Bootstrap-done source of truth") {
		t.Error("require_bootstrap_ready must document CR status.regions as SoT")
	}

	update, err := os.ReadFile("update.sh")
	if err != nil {
		t.Fatalf("reading update.sh: %v", err)
	}
	if !strings.Contains(string(update), "require_bootstrap_ready") {
		t.Error("update.sh must call require_bootstrap_ready")
	}
}

// TestWaitForPostgresDefaultIsShortened asserts common.sh's wait_for_postgres
// default upper bound is 15 attempts (2s sleep -> ~30s), down from the prior
// 90 attempts (~180s), and that it honors NOMINATIM_PG_WAIT_ATTEMPTS when set.
// CNPG cluster/database readiness (cnpgClusterReadyForJobs) is the primary
// gate before the Job is even created; this loop is last-mile only (R5).
func TestWaitForPostgresDefaultIsShortened(t *testing.T) {
	contents, err := os.ReadFile("common.sh")
	if err != nil {
		t.Fatalf("reading common.sh: %v", err)
	}
	script := string(contents)

	if !strings.Contains(script, "NOMINATIM_PG_WAIT_ATTEMPTS") {
		t.Error("common.sh wait_for_postgres must reference NOMINATIM_PG_WAIT_ATTEMPTS for operator/user override")
	}
	if !strings.Contains(script, `NOMINATIM_PG_WAIT_ATTEMPTS:-15`) {
		t.Error(`common.sh wait_for_postgres must default to 15 attempts ` +
			`via "${NOMINATIM_PG_WAIT_ATTEMPTS:-15}" (~30s at 2s/attempt)`)
	}
	if strings.Contains(script, "seq 1 90") {
		t.Error("common.sh wait_for_postgres must not retain the old 90-attempt (~180s) upper bound")
	}
}

// TestMigrateAndFreezeScriptContracts asserts Migrate/Freeze worker scripts match
// upstream Nominatim 4.5 admin docs (nominatim-5et.13 / 5et.18):
//
//	Migrate → nominatim admin --migrate (after image bump; stop updates first)
//	Freeze  → nominatim freeze (drop dynamic-update tables; no further OSM updates)
func TestMigrateAndFreezeScriptContracts(t *testing.T) {
	migrate, err := os.ReadFile("migrate.sh")
	if err != nil {
		t.Fatalf("reading migrate.sh: %v", err)
	}
	ms := string(migrate)
	if !strings.Contains(ms, "require_bootstrap_ready") {
		t.Error("migrate.sh must call require_bootstrap_ready")
	}
	if !strings.Contains(ms, "run_nominatim admin --migrate") {
		t.Error("migrate.sh must invoke run_nominatim admin --migrate")
	}

	freeze, err := os.ReadFile("freeze.sh")
	if err != nil {
		t.Fatalf("reading freeze.sh: %v", err)
	}
	fs := string(freeze)
	if !strings.Contains(fs, "require_bootstrap_ready") {
		t.Error("freeze.sh must call require_bootstrap_ready")
	}
	if !strings.Contains(fs, "run_nominatim freeze") {
		t.Error("freeze.sh must invoke run_nominatim freeze")
	}

	entrypoint, err := os.ReadFile("entrypoint.sh")
	if err != nil {
		t.Fatalf("reading entrypoint.sh: %v", err)
	}
	ep := string(entrypoint)
	for _, needle := range []string{"Migrate", "migrate.sh", "Freeze", "freeze.sh"} {
		if !strings.Contains(ep, needle) {
			t.Errorf("entrypoint.sh must dispatch Migrate/Freeze (missing %q)", needle)
		}
	}
}

// TestRefreshScriptContract asserts Refresh runs nominatim refresh admin tasks
// after bootstrap readiness (nominatim-5et.12), not on the API boot path.
func TestRefreshScriptContract(t *testing.T) {
	refresh, err := os.ReadFile("refresh.sh")
	if err != nil {
		t.Fatalf("reading refresh.sh: %v", err)
	}
	script := string(refresh)
	if !strings.Contains(script, "require_bootstrap_ready") {
		t.Error("refresh.sh must call require_bootstrap_ready")
	}
	if !strings.Contains(script, "run_nominatim refresh") {
		t.Error("refresh.sh must invoke run_nominatim refresh")
	}
	for _, flag := range []string{"--postcodes", "--word-counts", "--functions", "--importance"} {
		if !strings.Contains(script, flag) {
			t.Errorf("refresh.sh default task set must include %s", flag)
		}
	}
	if !strings.Contains(script, "NOMINATIM_REFRESH_TASKS") {
		t.Error("refresh.sh must honor NOMINATIM_REFRESH_TASKS override")
	}

	entrypoint, err := os.ReadFile("entrypoint.sh")
	if err != nil {
		t.Fatalf("reading entrypoint.sh: %v", err)
	}
	ep := string(entrypoint)
	if !strings.Contains(ep, "Refresh") {
		t.Error("entrypoint.sh must dispatch Refresh")
	}
	if !strings.Contains(ep, "refresh.sh") {
		t.Error("entrypoint.sh must exec refresh.sh for Refresh")
	}
}

// TestAuxDataDownloadContract asserts common.sh downloads aux datasets when
// NOMINATIM_AUX_* env vars are set and only symlinks present staging files.
func TestAuxDataDownloadContract(t *testing.T) {
	common, err := os.ReadFile("common.sh")
	if err != nil {
		t.Fatalf("reading common.sh: %v", err)
	}
	script := string(common)

	for _, needle := range []string{
		"ensure_aux_data_downloads",
		"materialize_aux_file_to_project",
		"NOMINATIM_AUX_WIKIMEDIA_IMPORTANCE",
		"NOMINATIM_AUX_SECONDARY_IMPORTANCE",
		"NOMINATIM_AUX_US_POSTCODES",
		"https://nominatim.org/data",
		"wikimedia-secondary-importance.sql.gz",
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("common.sh missing aux data contract %q", needle)
		}
	}
	if strings.Contains(script, `link_staging_name "wikimedia-importance.csv.gz"`) {
		t.Error("common.sh must not symlink aux files into project; materialize durable copies instead")
	}

	refresh, err := os.ReadFile("refresh.sh")
	if err != nil {
		t.Fatalf("reading refresh.sh: %v", err)
	}
	rs := string(refresh)
	if !strings.Contains(rs, "--wiki-data") || !strings.Contains(rs, "--secondary-importance") {
		t.Error("refresh.sh must backfill wiki/secondary importance when aux files are present")
	}

	report, err := os.ReadFile("report-sequence.sh")
	if err != nil {
		t.Fatalf("reading report-sequence.sh: %v", err)
	}
	if !strings.Contains(string(report), "aux-data.json") {
		t.Error("report-sequence.sh must publish aux-data.json for status.auxData")
	}
}

// TestReportSequenceScriptIsProbeOnly asserts sequence reporting is a dedicated
// script for the operator probe Job, not wired into Update/Bootstrap (5et.35.3).
func TestReportSequenceScriptIsProbeOnly(t *testing.T) {
	report, err := os.ReadFile("report-sequence.sh")
	if err != nil {
		t.Fatalf("reading report-sequence.sh: %v", err)
	}
	if !strings.Contains(string(report), "report.json") {
		t.Error("report-sequence.sh must patch ConfigMap report.json")
	}
	for _, name := range []string{"update.sh", "bootstrap.sh", "add-regions.sh"} {
		contents, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if strings.Contains(string(contents), "report-sequence") {
			t.Errorf("%s must not invoke report-sequence (operator probe owns that)", name)
		}
	}
}
