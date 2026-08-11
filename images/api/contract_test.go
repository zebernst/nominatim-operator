// Package api holds contract tests over the API image entrypoint.
package api

import (
	"os"
	"strings"
	"testing"
)

// TestEntrypointIsReadOnlyServingPlane asserts the API entrypoint does not mutate
// shared project state or run admin refresh (nominatim-5et.35.1).
func TestEntrypointIsReadOnlyServingPlane(t *testing.T) {
	contents, err := os.ReadFile("entrypoint.sh")
	if err != nil {
		t.Fatalf("reading entrypoint.sh: %v", err)
	}
	script := string(contents)

	for _, forbidden := range []string{
		"import-finished",
		"IMPORT_FINISHED",
		"refresh --functions",
		"NOMINATIM_FLATNODE_FILE",
		"ENV_DEFAULTS",
		"sed -i",
		"link_staging",
		"IMPORT_STAGING",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("entrypoint must not reference %q (read-only serving plane)", forbidden)
		}
	}
	if !strings.Contains(script, "public.placex") {
		t.Error("entrypoint must still refuse to start without placex")
	}
	if !strings.Contains(script, "gunicorn") {
		t.Error("entrypoint must start gunicorn")
	}
}

// TestEntrypointDerivesWorkersFromCgroup asserts Gunicorn workers prefer cgroup
// CPU quota over bare nproc (nominatim-5et.14).
func TestEntrypointDerivesWorkersFromCgroup(t *testing.T) {
	contents, err := os.ReadFile("entrypoint.sh")
	if err != nil {
		t.Fatalf("reading entrypoint.sh: %v", err)
	}
	script := string(contents)
	for _, needle := range []string{
		"default_gunicorn_workers",
		"/sys/fs/cgroup/cpu.max",
		"cpu.cfs_quota_us",
		"GUNICORN_WORKERS",
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("entrypoint missing %q", needle)
		}
	}
}
