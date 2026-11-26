package testutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rubnogueira/mise-brew/internal/paths"
)

// FakePrefix points the Homebrew prefix at a fresh temporary directory for t.
func FakePrefix(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	paths.PrefixCache = dir
	t.Cleanup(func() { paths.PrefixCache = "" })
	return dir
}

// Write creates p (and its parent directories) with content, failing t on error.
func Write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// MakeKeg creates Cellar/name/version with the given files and a minimal receipt.
func MakeKeg(t *testing.T, name, version string, files ...string) {
	t.Helper()
	keg := filepath.Join(paths.Cellar(), name, version)
	for _, f := range files {
		Write(t, filepath.Join(keg, f), "x")
	}
	Write(t, filepath.Join(keg, "INSTALL_RECEIPT.json"), `{"homebrew_version":"5.1.15 (mise)","runtime_dependencies":[]}`)
}
