package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeAPI points mise-brew at a temporary mise cache holding the given API
// records, with no configured taps, and resets every lookup cache.
func fakeAPI(t *testing.T, formulae, casks []string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MISE_CACHE_DIR", dir)
	write(t, filepath.Join(apiDir(), "formula.json"), "["+strings.Join(formulae, ",")+"]")
	write(t, filepath.Join(apiDir(), "cask.json"), "["+strings.Join(casks, ",")+"]")
	reset := func() {
		formulaAPI, caskAPI = &apiSource{kind: "formula"}, &apiSource{kind: "cask"}
		formulaCache, caskCache, tapFormulaCache = map[string]*Formula{}, map[string]*Cask{}, map[string]*Formula{}
		tapIndexCache = map[string]*tapIndex{}
		aliasIndex, caskOldTokenIndex, tapURLCache = nil, nil, map[string]string{}
	}
	reset()
	t.Cleanup(func() { reset(); tapURLCache = nil })
	return dir
}

// fakeTap caches a tap index and its Ruby sources (fresh, so nothing is fetched).
func fakeTap(t *testing.T, tap string, files map[string]string) {
	t.Helper()
	owner, repo := tapRepo(tap)
	ti := tapIndex{Fetched: time.Now(), Owner: owner, Repo: repo, Formulae: map[string]string{}, Casks: map[string]string{}}
	for p, src := range files {
		name := strings.TrimSuffix(filepath.Base(p), ".rb")
		if strings.HasPrefix(p, "Casks/") {
			ti.Casks[name] = p
		} else {
			ti.Formulae[name] = p
		}
		write(t, filepath.Join(miseBrewCacheDir(), "taps", owner, repo, p), src)
	}
	b, _ := json.Marshal(ti)
	write(t, filepath.Join(miseBrewCacheDir(), "taps", owner, repo+".json"), string(b))
}

// captureRun runs a brew command and returns its stdout and exit code.
func captureRun(t *testing.T, args ...string) (string, int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	code := run(args)
	w.Close()
	os.Stdout = old
	return <-done, code
}

func depsFixture(t *testing.T) {
	fakePrefix(t)
	fakeAPI(t, []string{
		`{"name":"zlib","full_name":"zlib","tap":"homebrew/core","dependencies":[],"versions":{"stable":"1.3"}}`,
		`{"name":"pcre2","full_name":"pcre2","tap":"homebrew/core","dependencies":[],"versions":{"stable":"10"}}`,
		`{"name":"git","full_name":"git","tap":"homebrew/core","dependencies":["pcre2"],"versions":{"stable":"2.50"}}`,
	}, []string{
		`{"token":"app","full_token":"app","tap":"homebrew/cask","version":"1","depends_on":{"formula":["git"]}}`,
	})
	fakeTap(t, "acme/tools", map[string]string{
		"Formula/widget.rb": "class Widget < Formula\n  url \"https://x/widget-1.0.tar.gz\"\n  depends_on \"lib\"\nend\n",
		"Formula/lib.rb":    "class Lib < Formula\n  url \"https://x/lib-1.0.tar.gz\"\n  depends_on \"zlib\"\nend\n",
		"Formula/git.rb":    "class Git < Formula\n  url \"https://x/git-9.0.tar.gz\"\n  depends_on \"zlib\"\nend\n",
		"Casks/thing.rb":    "cask \"thing\" do\n  version \"1.0\"\n  url \"https://x/thing-#{version}.dmg\"\n  depends_on formula: \"acme/tools/widget\"\nend\n",
	})
}

func TestDepsTapCask(t *testing.T) { // finding 1
	depsFixture(t)
	out, code := captureRun(t, "deps", "acme/tools/thing")
	if code != 0 || out != "acme/tools/lib\nacme/tools/widget\nzlib\n" {
		t.Errorf("deps tap cask: code=%d out=%q", code, out)
	}
	if c := lookupCask("acme/tools/thing"); c == nil || c.URL != "https://x/thing-1.0.dmg" {
		t.Errorf("tap cask url not expanded: %+v", c)
	}
}

func TestDepsTapFormula(t *testing.T) { // finding 2
	depsFixture(t)
	if out, code := captureRun(t, "deps", "acme/tools/widget"); code != 0 || out != "acme/tools/lib\nzlib\n" {
		t.Errorf("deps acme/tools/widget: code=%d out=%q", code, out)
	}
	if out, _ := captureRun(t, "deps", "acme/tools/git"); out != "zlib\n" {
		t.Errorf("deps acme/tools/git must not use core git: %q", out)
	}
	if out, _ := captureRun(t, "deps", "git"); out != "pcre2\n" {
		t.Errorf("deps git: %q", out)
	}
	if out, _ := captureRun(t, "deps", "--tree", "acme/tools/widget"); !strings.Contains(out, "acme/tools/widget\n└── acme/tools/lib\n    └── zlib") {
		t.Errorf("deps --tree: %q", out)
	}
}

func TestDepsGraphCask(t *testing.T) { // findings 6, 12
	depsFixture(t)
	out, _ := captureRun(t, "deps", "--dot", "--cask", "app")
	if !strings.Contains(out, `"app" -> "git"`) || !strings.Contains(out, `"git" -> "pcre2"`) {
		t.Errorf("cask graph: %q", out)
	}
	if got := dotQuote(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("dotQuote = %s", got)
	}
	if got := jsString("</script><b>&"); strings.ContainsAny(got, "<>&") {
		t.Errorf("jsString = %s", got)
	}
}

func TestInfoJSONTapFormula(t *testing.T) { // finding 3
	depsFixture(t)
	out, code := captureRun(t, "info", "--json=v2", "acme/tools/git")
	var v struct {
		Formulae []map[string]any `json:"formulae"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || code != 0 || len(v.Formulae) != 1 {
		t.Fatalf("code=%d err=%v out=%s", code, err, out)
	}
	f := v.Formulae[0]
	if f["full_name"] != "acme/tools/git" || f["tap"] != "acme/tools" || f["versions"].(map[string]any)["stable"] != "9.0" {
		t.Errorf("tap formula JSON: %v", f)
	}
	if deps, _ := f["dependencies"].([]any); len(deps) != 1 || deps[0] != "zlib" {
		t.Errorf("dependencies = %v", f["dependencies"])
	}
	if b, _ := f["build_dependencies"].([]any); b == nil {
		t.Errorf("build_dependencies should be [] not %v", f["build_dependencies"])
	}
}

func TestCaskVariations(t *testing.T) { // finding 5
	t.Setenv("MISEBREW_BOTTLE_TAG", "sonoma")
	rec := `{"token":"x","version":"2.0","url":"https://x/arm.dmg","sha256":"aa",` +
		`"artifacts":[{"app":["X.app"]},{"uninstall":[{"quit":"x.arm"}]}],` +
		`"variations":{"sonoma":{"url":"https://x/intel.dmg","sha256":"bb","artifacts":[{"app":["X.app"]},{"uninstall":[{"quit":"x.intel"}]}]},"arm64_sonoma":{"url":"nope"}}}`
	var c Cask
	if err := json.Unmarshal(mergeVariationsRaw(json.RawMessage(rec)), &c); err != nil {
		t.Fatal(err)
	}
	if c.URL != "https://x/intel.dmg" || c.SHA256 != "bb" || c.Version != "2.0" || !strings.Contains(string(c.Artifacts[1]), "x.intel") {
		t.Errorf("variation not applied: %+v", c)
	}
	fakeAPI(t, nil, []string{rec})
	if c := lookupCask("x"); c == nil || c.URL != "https://x/intel.dmg" {
		t.Errorf("lookupCask ignores variations: %+v", c)
	}
	if m := rawCaskMap("x"); m == nil || m["url"] != "https://x/intel.dmg" || m["variations"] != nil {
		t.Errorf("rawCaskMap = %v", m)
	}
	t.Setenv("MISEBREW_BOTTLE_TAG", "arm64_golden_gate")
	if out := mergeVariationsRaw(json.RawMessage(rec)); !strings.Contains(string(out), "arm.dmg") || strings.Contains(string(out), "variations") {
		t.Errorf("no matching variation: %s", out)
	}
}

func TestCaskOldTokens(t *testing.T) { // finding 4
	fakeAPI(t, nil, []string{`{"token":"new","old_tokens":["old"],"version":"1"}`})
	if c := lookupCask("old"); c == nil || c.Token != "new" {
		t.Errorf("old token: %+v", c)
	}
	if lookupCask("missing") != nil {
		t.Error("unknown token resolved")
	}
}

func receiptWithDeps(t *testing.T, name, version string, deps ...string) {
	t.Helper()
	var rd []RuntimeDep
	for _, d := range deps {
		rd = append(rd, RuntimeDep{FullName: d, Version: "1", PkgVersion: "1_2", Declared: true})
	}
	b, _ := json.Marshal(Receipt{RuntimeDependencies: rd})
	write(t, filepath.Join(kegPath(name, version), "INSTALL_RECEIPT.json"), string(b))
}

func TestMissing(t *testing.T) { // finding 7
	fakePrefix(t)
	receiptWithDeps(t, "a", "1", "b", "c")
	receiptWithDeps(t, "b", "1")
	if out, code := captureRun(t, "missing", "a"); code != 1 || out != "c\n" {
		t.Errorf("missing a: code=%d out=%q", code, out)
	}
	if out, _ := captureRun(t, "missing", "--hide=b", "a"); out != "b c\n" {
		t.Errorf("missing --hide=b a: %q", out)
	}
	if out, _ := captureRun(t, "missing"); out != "a: c\n" {
		t.Errorf("missing (all): %q", out)
	}
}

func TestUsesUnknownAndCask(t *testing.T) { // finding 8
	depsFixture(t)
	if out, code := captureRun(t, "uses", "nosuchformula"); code != 0 || out != "" {
		t.Errorf("uses unknown: code=%d out=%q", code, out)
	}
	if out, _ := captureRun(t, "uses", "--cask", "git"); out != "app\n" {
		t.Errorf("uses --cask git: %q", out)
	}
	if out, _ := captureRun(t, "uses", "git"); out != "app\n" {
		t.Errorf("uses git: %q", out)
	}
	if out, _ := captureRun(t, "uses", "pcre2"); out != "git\n" {
		t.Errorf("uses pcre2: %q", out)
	}
}

func TestRawIndexBounds(t *testing.T) { // finding 11
	fakeAPI(t, []string{`{"name":"a"}`, `{"name":"b"}`}, nil)
	data, _ := os.ReadFile(formulaAPI.jsonPath())
	size := int64(len(data))
	off := int64(strings.Index(string(data), `{"name":"b"}`))
	for _, loc := range [][2]int64{{-1, 5}, {0, 0}, {size - 2, 10}, {0, 1 << 40}, {1 << 62, 1 << 62}, {off, 5}} {
		idx, _ := json.Marshal(map[string]any{"source_size": size, "formulae": map[string][2]int64{"a": {1, 12}, "b": loc}})
		write(t, formulaAPI.indexPath(), string(idx))
		formulaAPI = &apiSource{kind: "formula"}
		raw, ok := formulaAPI.Raw("b")
		if !ok || string(raw) != `{"name":"b"}` {
			t.Errorf("loc %v: got %q %v", loc, raw, ok)
		}
	}
}

func TestTapRepo(t *testing.T) { // finding 13
	fakeAPI(t, nil, nil)
	tapURLCache = map[string]string{
		"a/dots":   "https://github.com/a/homebrew-my.tools.git",
		"a/ssh":    "git@github.com:a/b.git",
		"a/gitlab": "https://gitlab.com/a/homebrew-x",
		"a/sneaky": "https://evil.example/github.com/a/x",
	}
	cases := map[string]string{"a/dots": "a/homebrew-my.tools", "a/ssh": "a/b", "a/gitlab": "/", "a/sneaky": "/", "a/plain": "a/homebrew-plain"}
	for tap, want := range cases {
		if o, r := tapRepo(tap); o+"/"+r != want {
			t.Errorf("tapRepo(%s) = %s/%s, want %s", tap, o, r, want)
		}
	}
	if loadTapIndex("a/gitlab") != nil {
		t.Error("non-GitHub tap must not be read from github.com")
	}
}

func TestParseTapCaveats(t *testing.T) { // finding 14a
	src := `class Foo < Formula
  url "https://x/foo-1.0.tar.gz"
  def caveats
    "Run foo --init."
  end
  def install
    bin.install "foo"
  end
  test do
    (testpath/"t.rb").write <<~EOS
      puts 1
    EOS
  end
end
`
	f := parseTapFormula(src, "acme/tools", "foo", "Formula/foo.rb")
	if f.Caveats == nil || *f.Caveats != "Run foo --init." {
		t.Errorf("string caveats = %v", f.Caveats)
	}
	src2 := `class Foo < Formula
  def caveats
    <<~CAVEATS
      Mention EOS here.
        Indented.
    CAVEATS
  end
end
`
	f = parseTapFormula(src2, "acme/tools", "foo", "Formula/foo.rb")
	if f.Caveats == nil || *f.Caveats != "Mention EOS here.\n  Indented." {
		t.Errorf("heredoc caveats = %q", *f.Caveats)
	}
	f = parseTapFormula("class Foo < Formula\n  def caveats\n    s = \"a\"\n    s\n  end\n  test do\n    x <<~EOS\n    y\n    EOS\n  end\nend\n", "acme/tools", "foo", "Formula/foo.rb")
	if f.Caveats != nil {
		t.Errorf("caveats must not run into other heredocs: %q", *f.Caveats)
	}
}

const goreleaserFormula = `# typed: false
class Tool < Formula
  desc "A tool"
  homepage "https://example.com"
  version "1.2.3"

  on_macos do
    if Hardware::CPU.intel?
      url "https://x/tool_darwin_amd64.tar.gz"
      sha256 "1111111111111111111111111111111111111111111111111111111111111111"
    end
    on_arm do
      url "https://x/tool_darwin_arm64.tar.gz"
      sha256 "2222222222222222222222222222222222222222222222222222222222222222"
    end
    depends_on "macdep"
  end

  on_linux do
    on_intel do
      url "https://x/tool_linux_amd64.tar.gz"
      sha256 "3333333333333333333333333333333333333333333333333333333333333333"
    end
    depends_on "glibc"
  end

  on_sonoma :or_newer do
    depends_on "newdep"
  end

  head do
    url "https://x/tool.git"
    depends_on "go" => :build
  end

  depends_on "../../etc"
  depends_on "acme/tools/lib"

  resource "extra" do
    url "https://x/extra.tar.gz"
  end

  def install
    bin.install "tool"
  end
end
`

func TestParseTapPlatformBlocks(t *testing.T) { // findings 14b, 14c
	cases := []struct {
		tag, url, sha string
		deps          string
	}{
		{"arm64_sonoma", "https://x/tool_darwin_arm64.tar.gz", "2222", "macdep,newdep,acme/tools/lib"},
		{"ventura", "https://x/tool_darwin_amd64.tar.gz", "1111", "macdep,acme/tools/lib"},
		{"x86_64_linux", "https://x/tool_linux_amd64.tar.gz", "3333", "glibc,acme/tools/lib"},
	}
	for _, c := range cases {
		t.Setenv("MISEBREW_BOTTLE_TAG", c.tag)
		f := parseTapFormula(goreleaserFormula, "acme/tools", "tool", "Formula/tool.rb")
		if f.URLs.Stable.URL != c.url || !strings.HasPrefix(f.URLs.Stable.Checksum, c.sha) {
			t.Errorf("%s: url=%q sha=%q", c.tag, f.URLs.Stable.URL, f.URLs.Stable.Checksum)
		}
		if got := strings.Join(f.Dependencies, ","); got != c.deps || len(f.BuildDependencies) != 0 {
			t.Errorf("%s: deps=%s build=%v", c.tag, got, f.BuildDependencies)
		}
		if f.Desc != "A tool" || f.Versions.Stable != "1.2.3" {
			t.Errorf("%s: desc/version %q %q", c.tag, f.Desc, f.Versions.Stable)
		}
	}
}

func TestParseTapInterpolation(t *testing.T) { // finding 14d
	src := `cask "thing" do
  arch arm: "arm64", intel: "x64"

  version "1.2.3,456"
  sha256 arm:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
         intel: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

  url "https://x/#{version.csv.first}/Thing-#{version.before_comma}-#{version.csv.second}-#{arch}-v#{version.major}.#{version.minor}.dmg"
  name "Thing"
  app "Thing.app"
  caveats <<~EOS
    Thing caveat.
  EOS
end
`
	t.Setenv("MISEBREW_BOTTLE_TAG", "arm64_sonoma")
	c := parseTapCask(src, "acme/tools", "thing", "Casks/thing.rb")
	if c.URL != "https://x/1.2.3/Thing-1.2.3-456-arm64-v1.2.dmg" || !strings.HasPrefix(c.SHA256, "aaaa") {
		t.Errorf("arm: url=%q sha=%q", c.URL, c.SHA256)
	}
	if c.Caveats == nil || *c.Caveats != "Thing caveat." {
		t.Errorf("cask caveats = %v", c.Caveats)
	}
	t.Setenv("MISEBREW_BOTTLE_TAG", "sonoma")
	c = parseTapCask(src, "acme/tools", "thing", "Casks/thing.rb")
	if !strings.Contains(c.URL, "-x64-") || !strings.HasPrefix(c.SHA256, "bbbb") {
		t.Errorf("intel: url=%q sha=%q", c.URL, c.SHA256)
	}
	c = parseTapCask("cask \"t\" do\n  version \"1\"\n  url \"https://x/#{language}/t.dmg\"\nend\n", "acme/tools", "t", "Casks/t.rb")
	if c.URL != "" {
		t.Errorf("unexpanded interpolation must leave url empty: %q", c.URL)
	}
	f := parseTapFormula("class T < Formula\n  version \"../../x\"\n  url \"https://x/t-#{version}.tgz\"\nend\n", "acme/tools", "t", "Formula/t.rb")
	if f.Versions.Stable != "" || f.URLs.Stable.URL != "" {
		t.Errorf("unsafe version accepted: %q %q", f.Versions.Stable, f.URLs.Stable.URL)
	}
	f = parseTapFormula("class T < Formula\n  version \"2.1\"\n  url \"https://x/t-#{version}.tgz\"\n  sha256 \"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\"\nend\n", "acme/tools", "t", "Formula/t.rb")
	if f.URLs.Stable.URL != "https://x/t-2.1.tgz" || !strings.HasPrefix(f.URLs.Stable.Checksum, "cccc") {
		t.Errorf("formula url=%q sha=%q", f.URLs.Stable.URL, f.URLs.Stable.Checksum)
	}
}

func TestRuntimeDepRevision(t *testing.T) { // finding 9
	depsFixture(t)
	receiptWithDeps(t, "git", "2.50", "pcre2")
	out, _ := captureRun(t, "info", "--json=v2", "git")
	if !strings.Contains(out, `"revision": 2`) {
		t.Errorf("runtime_dependencies revision not derived from pkg_version: %s", out)
	}
}

func TestConfiguredTapsFromFilesTOMLForms(t *testing.T) {
	isolateMise(t, "")
	cfg := filepath.Join(t.TempDir(), "mise.toml")
	write(t, cfg, `[bootstrap.brew.taps]
"Acme/Tools" = "https://github.com/acme/homebrew-tools"
"x/y" = { url = "https://github.com/x/homebrew-y" }

[[tasks.build.steps]]
"bootstrap.brew.taps.evil/one" = "not a tap"

[env]
NOTE = """
[bootstrap.brew.taps]
"evil/two" = "inside a string"
"""
`)
	t.Setenv("MISEBREW_CONFIG", cfg)
	got := configuredTapsFromFiles()
	if got["acme/tools"] != "https://github.com/acme/homebrew-tools" || got["x/y"] != "https://github.com/x/homebrew-y" {
		t.Errorf("taps = %v", got)
	}
	if _, ok := got["evil/one"]; ok {
		t.Errorf("array-of-tables key read as tap: %v", got)
	}
	if _, ok := got["evil/two"]; ok {
		t.Errorf("multi-line string read as tap: %v", got)
	}
}
