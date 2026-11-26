package brewfile

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rubnogueira/mise-brew/internal/testutil"
)

func TestParseBrewfile(t *testing.T) {
	src := `# Brewfile
tap "homebrew/bundle"
tap "acme/tools", "https://example.com/acme/tools.git"
brew "jq"
brew "postgresql@17", restart_service: true
brew("wget") # parens
cask "firefox"
mas "Xcode", id: 497799835
cargo "ripgrep"
brew "linux-only" if OS.linux?
if OS.mac?
  brew "mac-only"
else
  brew "not-mac"
end
whatever "x"
`
	es := parseBrewfile(strings.NewReader(src), "Brewfile")
	got := map[string]BrewfileEntry{}
	var names []string
	for _, e := range es {
		got[e.Kind+":"+e.Name] = e
		names = append(names, e.Kind+":"+e.Name)
	}
	for _, want := range []string{"tap:homebrew/bundle", "tap:acme/tools", "brew:jq", "brew:postgresql@17", "brew:wget", "cask:firefox", "mas:Xcode", "cargo:ripgrep", "brew:mac-only"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s (got %v)", want, names)
		}
	}
	for _, bad := range []string{"brew:linux-only", "brew:not-mac"} {
		if _, ok := got[bad]; ok {
			t.Errorf("%s should be excluded on macOS", bad)
		}
	}
	if got["mas:Xcode"].ID != "497799835" || PackageKey(got["mas:Xcode"]) != "mas:497799835" {
		t.Errorf("mas id = %q", got["mas:Xcode"].ID)
	}
	if got["tap:acme/tools"].Extra != "https://example.com/acme/tools.git" {
		t.Errorf("tap url = %q", got["tap:acme/tools"].Extra)
	}
	if !got["brew:postgresql@17"].Restart {
		t.Error("restart_service not parsed")
	}
	if ToolKey(got["cargo:ripgrep"]) != "cargo:ripgrep" {
		t.Error("cargo tool key")
	}
}

func TestRubyBrewfile(t *testing.T) {
	ruby := FindRuby()
	if ruby == "" {
		t.Skip("ruby >= 2.6 not available")
	}
	dir := t.TempDir()
	bf := filepath.Join(dir, "Brewfile")
	testutil.Write(t, bf, `cask_args appdir: "~/Applications"
%w[jq wget].each { |f| brew f }
brew "postgresql@17", restart_service: :changed, link: false, conflicts_with: ["postgresql@16"]
cask "firefox", args: { appdir: "/Applications/Web" }
mas "Xcode", id: 497799835
brew "linux-thing" if OS.linux?
vscode "golang.go"
`)
	es, err := parseBrewfileRuby(ruby, bf)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]BrewfileEntry{}
	for _, e := range es {
		got[e.Kind+":"+e.Name] = e
	}
	for _, k := range []string{"brew:jq", "brew:wget", "brew:postgresql@17", "cask:firefox", "mas:Xcode", "vscode:golang.go"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing %s", k)
		}
	}
	if runtime.GOOS == "darwin" {
		if _, ok := got["brew:linux-thing"]; ok {
			t.Error("OS.linux? guard ignored")
		}
	}
	pg := got["brew:postgresql@17"]
	if pg.OptString("restart_service") != "changed" || pg.OptStrings("conflicts_with")[0] != "postgresql@16" {
		t.Errorf("options = %v", pg.Options)
	}
	if l, ok := pg.OptBool("link"); !ok || l {
		t.Error("link: false not parsed")
	}
	if got["mas:Xcode"].ID != "497799835" || fmt.Sprint(BundleCaskArgs["appdir"]) != "~/Applications" {
		t.Errorf("mas id %q cask_args %v", got["mas:Xcode"].ID, BundleCaskArgs)
	}
}
