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
