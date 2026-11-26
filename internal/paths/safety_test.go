package paths

import (
	"testing"
)

func TestMiseCacheDirHonoursXDG(t *testing.T) {
	t.Setenv("MISE_CACHE_DIR", "")
	t.Setenv("XDG_CACHE_HOME", "/xdg/cache")
	if d := miseCacheDir(); d != "/xdg/cache/mise" {
		t.Errorf("miseCacheDir() = %q", d)
	}
	t.Setenv("MISE_CACHE_DIR", "/m/cache")
	if d := miseCacheDir(); d != "/m/cache" {
		t.Errorf("miseCacheDir() = %q", d)
	}
}
