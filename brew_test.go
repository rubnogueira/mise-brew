package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fakePrefix(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prefixCache = dir
	t.Cleanup(func() { prefixCache = "" })
	return dir
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func makeKeg(t *testing.T, name, version string, files ...string) {
	t.Helper()
	keg := kegPath(name, version)
	for _, f := range files {
		write(t, filepath.Join(keg, f), "x")
	}
	write(t, filepath.Join(keg, "INSTALL_RECEIPT.json"), `{"homebrew_version":"5.1.15 (mise)","runtime_dependencies":[]}`)
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2", "1.10", -1},
		{"1.2_1", "1.2", 1},
		{"1.2", "1.2_1", -1},
		{"2.0rc1", "2.0", -1},
		{"20261022", "20260901", 1},
		{"3.6.4_1", "3.6.4_1", 0},
		{"1.0.0", "1.0", 0}, // Homebrew pads with zeros
		{"1.0beta2", "1.0rc1", -1},
		{"1.0p1", "1.0", 1},
		{"1.0", "1.0post1", -1},
		{"2.73", "2.8", 1},
		{"1.4.0_3", "1.4.0", 1},
		{"HEAD-abc123", "9.9", 1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestLinkUnlinkRoundTrip(t *testing.T) {
	p := fakePrefix(t)
	makeKeg(t, "foo", "1.0", "bin/foo", "lib/libfoo.dylib", "share/man/man1/foo.1", "share/foo/data.txt")
	// A file from another keg sharing share/man/man1 must survive unlink.
	makeKeg(t, "bar", "2.0", "share/man/man1/bar.1")
	if _, _, err := linkKeg("bar", "2.0", false, false); err != nil {
		t.Fatal(err)
	}
	n, conflicts, err := linkKeg("foo", "1.0", false, false)
	if err != nil || len(conflicts) > 0 {
		t.Fatalf("link: n=%d conflicts=%v err=%v", n, conflicts, err)
	}
	if n != 4 {
		t.Errorf("created %d links, want 4", n)
	}
	target, err := os.Readlink(filepath.Join(p, "bin", "foo"))
	if err != nil || target != "../Cellar/foo/1.0/bin/foo" {
		t.Errorf("bin/foo -> %q (%v)", target, err)
	}
	if linkedVersion("foo") != "1.0" || readLinkVersion(filepath.Join(optDir(), "foo")) != "1.0" {
		t.Error("linked/opt records not written")
	}
	removed, _ := unlinkKeg("foo", "1.0", false)
	if removed != 4 {
		t.Errorf("removed %d, want 4", removed)
	}
	if fileExists(filepath.Join(p, "bin", "foo")) || fileExists(filepath.Join(p, "share", "foo")) {
		t.Error("foo links or empty dirs left behind")
	}
	if !fileExists(filepath.Join(p, "share", "man", "man1", "bar.1")) {
		t.Error("unlink removed another keg's link")
	}
	if !isDir(filepath.Join(p, "bin")) {
		t.Error("top-level bin dir must not be removed")
	}
}

func TestLinkConflictAndOverwrite(t *testing.T) {
	p := fakePrefix(t)
	makeKeg(t, "foo", "1.0", "bin/foo")
	write(t, filepath.Join(p, "bin", "foo"), "unmanaged")
	_, conflicts, _ := linkKeg("foo", "1.0", false, false)
	if len(conflicts) != 1 || conflicts[0].rel != "bin/foo" {
		t.Fatalf("conflicts=%v", conflicts)
	}
	if b, _ := os.ReadFile(filepath.Join(p, "bin", "foo")); string(b) != "unmanaged" {
		t.Error("conflicting file was modified without --overwrite")
	}
	if _, _, err := linkKeg("foo", "1.0", true, false); err != nil {
		t.Fatal(err)
	}
	if !resolvesInto(filepath.Join(p, "bin", "foo"), kegPath("foo", "1.0")) {
		t.Error("--overwrite did not link")
	}
}

func TestLinkExpandsForeignDirSymlink(t *testing.T) {
	p := fakePrefix(t)
	makeKeg(t, "a", "1", "share/common/a.txt")
	makeKeg(t, "b", "1", "share/common/b.txt")
	// keg a owns share/common as a directory symlink (Homebrew does this).
	os.MkdirAll(filepath.Join(p, "share"), 0o755)
	os.Symlink("../Cellar/a/1/share/common", filepath.Join(p, "share", "common"))
	if _, c, err := linkKeg("b", "1", false, false); err != nil || len(c) > 0 {
		t.Fatalf("link b: %v %v", c, err)
	}
	fi, _ := os.Lstat(filepath.Join(p, "share", "common"))
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("dir symlink not expanded")
	}
	for _, f := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(p, "share", "common", f)); err != nil {
			t.Errorf("%s not reachable: %v", f, err)
		}
	}
}

func TestRemoveKegsCleansRecords(t *testing.T) {
	p := fakePrefix(t)
	makeKeg(t, "foo", "1.0", "bin/foo")
	makeKeg(t, "foo", "2.0", "bin/foo")
	linkKeg("foo", "2.0", false, false)
	setRecordLink(pinnedDir(), "foo", "2.0")
	if err := removeKegs("foo", []string{"2.0"}); err != nil {
		t.Fatal(err)
	}
	if !isDir(kegPath("foo", "1.0")) || readLinkVersion(filepath.Join(optDir(), "foo")) != "1.0" {
		t.Error("opt should be repointed at the remaining keg")
	}
	if err := removeKegs("foo", kegVersions("foo")); err != nil {
		t.Fatal(err)
	}
	for _, x := range []string{"Cellar/foo", "opt/foo", "var/homebrew/linked/foo", "var/homebrew/pinned/foo", "bin/foo"} {
		if fileExists(filepath.Join(p, x)) {
			t.Errorf("%s left behind", x)
		}
	}
}

func TestDeclarationsEditThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real", "mise.toml")
	write(t, real, `# my config
[tools]
node = "24"

[bootstrap.packages]
"brew:jq" = "latest" # json
"brew:wget" = "latest"
# "brew:wget" = "old"
'brew:fd' = { version = "latest", os = ["macos"] }
"brew-cask:firefox" = "latest"
`)
	link := filepath.Join(dir, "conf.d", "mise.toml")
	os.MkdirAll(filepath.Dir(link), 0o755)
	os.Symlink(real, link)
	other := filepath.Join(dir, "dotted.toml")
	write(t, other, "bootstrap.packages.\"brew:wget\" = \"latest\"\n[bootstrap]\npackages.\"brew:jq\" = \"1\"\n")

	files := []string{link, other}
	d := findDeclarations("brew:wget", files)
	if len(d) != 2 || !d[0].Editable || !d[1].Editable {
		t.Fatalf("wget decls = %+v", d)
	}
	if fd := findDeclarations("brew:fd", files); len(fd) != 1 || !fd[0].Editable {
		t.Fatalf("fd decls = %+v", fd)
	}
	if jq := findDeclarations("brew:jq", files); len(jq) != 2 {
		t.Fatalf("jq decls = %+v", jq)
	}
	if err := removeDeclarations(d); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlinked config was replaced by a regular file")
	}
	b, _ := os.ReadFile(real)
	s := string(b)
	if strings.Contains(s, "\"brew:wget\" = \"latest\"") || !strings.Contains(s, "# \"brew:wget\" = \"old\"") || !strings.Contains(s, "brew:jq") {
		t.Errorf("unexpected result:\n%s", s)
	}
	if b, _ := os.ReadFile(other); strings.Contains(string(b), "wget") {
		t.Errorf("dotted key not removed:\n%s", b)
	}
	if findDeclarations("brew:firefox", files) != nil {
		t.Error("brew:firefox must not match brew-cask:firefox")
	}
}

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
	got := map[string]brewfileEntry{}
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
	if got["mas:Xcode"].ID != "497799835" || packageKey(got["mas:Xcode"]) != "mas:497799835" {
		t.Errorf("mas id = %q", got["mas:Xcode"].ID)
	}
	if got["tap:acme/tools"].Extra != "https://example.com/acme/tools.git" {
		t.Errorf("tap url = %q", got["tap:acme/tools"].Extra)
	}
	if !got["brew:postgresql@17"].Restart {
		t.Error("restart_service not parsed")
	}
	if toolKey(got["cargo:ripgrep"]) != "cargo:ripgrep" {
		t.Error("cargo tool key")
	}
}

func TestSplitBottleName(t *testing.T) {
	inst := map[string]string{"aws-c-common": "1.0.6", "aws-c": "9", "openssl@4": "4.1.0"}
	if n, v := splitBottleName("aws-c-common-1.0.4.tar.gz", inst); n != "aws-c-common" || v != "1.0.4" {
		t.Errorf("got %s %s", n, v)
	}
	if n, v := splitBottleName("openssl@4-4.1.0.tar.gz", inst); n != "openssl@4" || v != "4.1.0" {
		t.Errorf("got %s %s", n, v)
	}
	if n, _ := splitBottleName("deno-2.9.8.tar.gz", inst); n != "" {
		t.Errorf("uninstalled bottle matched %s", n)
	}
}

func TestParseArgs(t *testing.T) {
	o := parseArgs([]string{"install", "-qn", "--cask", "--json=v2", "--file", "Brewfile", "foo", "--", "-x"}, []string{"file"}, nil)
	if !o.Has("quiet") || !o.Has("dry-run") || !o.Has("cask") {
		t.Errorf("flags = %v", o.flags)
	}
	if v, _ := o.Val("json"); v != "v2" {
		t.Errorf("json = %q", v)
	}
	if v, _ := o.Val("file"); v != "Brewfile" {
		t.Errorf("file = %q", v)
	}
	if strings.Join(o.Pos, " ") != "install foo -x" {
		t.Errorf("pos = %v", o.Pos)
	}
}

func TestUndeclareRestore(t *testing.T) {
	isolateMise(t, "")
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mise.toml")
	orig := "[bootstrap.packages]\n\"brew:keep-me\" = \"latest\"\n\"brew-cask:mise-brew-test-cask\" = \"latest\"\n"
	write(t, cfg, orig)
	t.Setenv("MISEBREW_CONFIG", cfg)
	restore := undeclare("brew-cask:mise-brew-test-cask")
	b, _ := os.ReadFile(cfg)
	if strings.Contains(string(b), "mise-brew-test-cask") || !strings.Contains(string(b), "keep-me") {
		t.Fatalf("after undeclare:\n%s", b)
	}
	restore()
	if b, _ := os.ReadFile(cfg); string(b) != orig {
		t.Fatalf("after restore:\n%s", b)
	}
}

func TestRecursionGuard(t *testing.T) {
	t.Setenv(recursionEnv, "1")
	if code := run([]string{"install", "hello"}); code != 1 {
		t.Fatalf("nested install exit=%d, want 1", code)
	}
}

func TestFlagValidation(t *testing.T) {
	if code := run([]string{"list", "--bogus-flag"}); code != 1 {
		t.Errorf("unknown flag exit=%d, want 1", code)
	}
	if code := run([]string{"--prefix", "--installed", "--formula"}); code != 0 {
		t.Errorf("known flags exit=%d, want 0", code)
	}
	if specs := commandSpecs["desc"]; len(specs) == 0 || shortMap("desc")["d"] != "description" {
		t.Errorf("desc -d should mean --description, got %q", shortMap("desc")["d"])
	}
}

func TestApplyVariations(t *testing.T) {
	raw := []byte(`{"name":"x","dependencies":["a"],"variations":{"` + platformTag() + `":{"dependencies":["a","b"]}}}`)
	var f Formula
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	applyVariations(raw, &f)
	if strings.Join(f.Dependencies, ",") != "a,b" {
		t.Errorf("deps = %v", f.Dependencies)
	}
	m := map[string]any{"dependencies": []any{"a"}, "variations": map[string]any{platformTag(): map[string]any{"dependencies": []any{"z"}}}}
	applyVariationsMap(m, false)
	if _, ok := m["variations"]; ok || fmt.Sprint(m["dependencies"]) != "[z]" {
		t.Errorf("map = %v", m)
	}
}

func TestMacosBounds(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	f := &Formula{
		UsesFromMacos:       []json.RawMessage{json.RawMessage(`"zlib"`), json.RawMessage(`{"python":"build"}`)},
		UsesFromMacosBounds: []map[string]string{{"since": "big_sur"}, {"since": "99"}},
	}
	if macosDepApplies(f, 0) {
		t.Error("zlib is provided by macOS since big_sur")
	}
	if !macosDepApplies(f, 1) {
		t.Error("python is needed before macOS 99")
	}
}

func TestOutdatedIgnoresMiseSchemeZero(t *testing.T) {
	fakePrefix(t)
	makeKeg(t, "lcms", "2.20")
	makeKeg(t, "ossl", "3.6.4_1")
	same := &Formula{Name: "lcms", VersionScheme: 1}
	same.Versions.Stable = "2.20"
	if kegOutdated("lcms", same) {
		t.Error("identical version must not be outdated even with a scheme bump")
	}
	bumped := &Formula{Name: "ossl", VersionScheme: 1}
	bumped.Versions.Stable = "3.5.9"
	if !kegOutdated("ossl", bumped) {
		t.Error("version_scheme bump must make an older-scheme keg outdated")
	}
}

func TestParseTapFormula(t *testing.T) {
	src := `class Foo < Formula
  desc "Does \"foo\" things"
  homepage "https://example.com/foo"
  url "https://example.com/foo/archive/v1.4.2.tar.gz"
  sha256 "abc"
  license "MIT"
  revision 2

  depends_on "pkgconf" => :build
  depends_on "openssl@3"
  depends_on "python" => [:build, :test]

  def caveats
    <<~EOS
      Run foo --init first.
    EOS
  end
  service do
    run [opt_bin/"foo"]
  end
end
`
	f := parseTapFormula(src, "acme/tools", "foo", "Formula/foo.rb")
	if f.Desc != `Does "foo" things` || f.Versions.Stable != "1.4.2" || f.PkgVersion() != "1.4.2_2" || f.License != "MIT" {
		t.Errorf("parsed %+v", f)
	}
	if strings.Join(f.Dependencies, ",") != "openssl@3" || strings.Join(f.BuildDependencies, ",") != "pkgconf,python" {
		t.Errorf("deps %v build %v", f.Dependencies, f.BuildDependencies)
	}
	if f.Caveats == nil || !strings.Contains(*f.Caveats, "foo --init") || len(f.Service) == 0 {
		t.Error("caveats/service not parsed")
	}
	if v := versionFromURL("https://x.org/releases/bar-2.0.1-src.tar.xz"); v != "2.0.1" {
		t.Errorf("versionFromURL = %q", v)
	}
}

func TestNativeCaskUninstall(t *testing.T) {
	p := fakePrefix(t)
	room := filepath.Join(p, "Caskroom", "mbt-cask", "1.0")
	write(t, filepath.Join(room, "tool", "bin", "tool"), "#!/bin/sh\n")
	os.MkdirAll(filepath.Join(p, "bin"), 0o755)
	link := filepath.Join(p, "bin", "mbt-tool")
	os.Symlink(filepath.Join(room, "tool", "bin", "tool"), link)
	write(t, filepath.Join(room, ".mise-cask.toml"), `schema_version = 3
version = "1.0"
binaries = ["`+link+`"]
pkg_ids = []

[[targets]]
path = "`+link+`"
`)
	if err := nativeUninstallCask("mbt-cask", false, true); err != nil {
		t.Fatal(err)
	}
	if !fileExists(link) || !isDir(room) {
		t.Fatal("dry run removed files")
	}
	if err := nativeUninstallCask("mbt-cask", false, false); err != nil {
		t.Fatal(err)
	}
	if fileExists(link) || fileExists(filepath.Join(p, "Caskroom", "mbt-cask")) {
		t.Error("cask artifacts left behind")
	}
}

func TestCaskSafePaths(t *testing.T) {
	o := &caskOps{dry: true}
	got := o.safePaths([]string{"/", "/Applications", "~/Library", "relative/path"})
	if len(got) != 0 {
		t.Errorf("protected paths passed: %v", got)
	}
}

func TestAutoremoveCandidates(t *testing.T) {
	fakePrefix(t)
	makeKeg(t, "mbt-app", "1")
	makeKeg(t, "mbt-lib", "1")
	makeKeg(t, "mbt-orphan", "1")
	write(t, filepath.Join(kegPath("mbt-app", "1"), "INSTALL_RECEIPT.json"),
		`{"installed_on_request":true,"runtime_dependencies":[{"full_name":"mbt-lib","version":"1"}]}`)
	for _, n := range []string{"mbt-lib", "mbt-orphan"} {
		write(t, filepath.Join(kegPath(n, "1"), "INSTALL_RECEIPT.json"), `{"installed_on_request":false,"installed_as_dependency":true}`)
	}
	// Pretend mise reported an empty status so the test doesn't run the real mise.
	oldCache, oldErr := statusCache, statusErr
	statusCache, statusErr = map[string]managerStatus{}, nil
	t.Cleanup(func() { statusCache, statusErr = oldCache, oldErr })
	if got := autoremoveCandidates(); strings.Join(got, ",") != "mbt-orphan" {
		t.Errorf("candidates = %v", got)
	}
}

func TestRubyBrewfile(t *testing.T) {
	ruby := findRuby()
	if ruby == "" {
		t.Skip("ruby >= 3 not available")
	}
	dir := t.TempDir()
	bf := filepath.Join(dir, "Brewfile")
	write(t, bf, `cask_args appdir: "~/Applications"
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
	got := map[string]brewfileEntry{}
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
	if pg.optString("restart_service") != "changed" || pg.optStrings("conflicts_with")[0] != "postgresql@16" {
		t.Errorf("options = %v", pg.Options)
	}
	if l, ok := pg.optBool("link"); !ok || l {
		t.Error("link: false not parsed")
	}
	if got["mas:Xcode"].ID != "497799835" || fmt.Sprint(bundleCaskArgs["appdir"]) != "~/Applications" {
		t.Errorf("mas id %q cask_args %v", got["mas:Xcode"].ID, bundleCaskArgs)
	}
}

func TestMergeEnv(t *testing.T) {
	out := mergeEnv([]string{"PATH=/usr/bin", "HOME=/h"}, [][2]string{{"PATH", "/shims:/usr/bin"}})
	if strings.Join(out, ";") != "HOME=/h;PATH=/shims:/usr/bin" {
		t.Errorf("mergeEnv = %v", out)
	}
}
