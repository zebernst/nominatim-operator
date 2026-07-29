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
		t.Error("add-regions.sh must not reference NOMINATIM_IMPORT_MAX_REGIONS; the operator now chunks AddRegions and the worker must import every desired region")
	}
	if strings.Contains(script, "deferring remaining regions") {
		t.Error("add-regions.sh must not defer remaining regions; it must import every region in DESIRED_REGIONS not already imported")
	}
}

// TestAddRegionsKeepsBashBelts asserts the IMPORT_FINISHED and NOMINATIM_REGIONS
// guards remain in add-regions.sh even though the operator now enforces
// preconditions too (belt-and-suspenders per R6 in the worker guards plan).
func TestAddRegionsKeepsBashBelts(t *testing.T) {
	contents, err := os.ReadFile("add-regions.sh")
	if err != nil {
		t.Fatalf("reading add-regions.sh: %v", err)
	}
	script := string(contents)

	if !strings.Contains(script, "IMPORT_FINISHED") {
		t.Error("add-regions.sh must still guard on IMPORT_FINISHED (Bootstrap-done belt)")
	}
	if !strings.Contains(script, "NOMINATIM_REGIONS") {
		t.Error("add-regions.sh must still require NOMINATIM_REGIONS (regions belt)")
	}
}
