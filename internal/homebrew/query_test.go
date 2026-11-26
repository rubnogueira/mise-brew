package homebrew

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/rubnogueira/mise-brew/internal/testutil"
)

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
	f := ParseTapFormula(src, "acme/tools", "foo", "Formula/foo.rb")
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
	f = ParseTapFormula(src2, "acme/tools", "foo", "Formula/foo.rb")
	if f.Caveats == nil || *f.Caveats != "Mention EOS here.\n  Indented." {
		t.Errorf("heredoc caveats = %q", *f.Caveats)
	}
	f = ParseTapFormula("class Foo < Formula\n  def caveats\n    s = \"a\"\n    s\n  end\n  test do\n    x <<~EOS\n    y\n    EOS\n  end\nend\n", "acme/tools", "foo", "Formula/foo.rb")
	if f.Caveats != nil {
		t.Errorf("caveats must not run into other heredocs: %q", *f.Caveats)
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
	f := ParseTapFormula("class T < Formula\n  version \"../../x\"\n  url \"https://x/t-#{version}.tgz\"\nend\n", "acme/tools", "t", "Formula/t.rb")
	if f.Versions.Stable != "" || f.URLs.Stable.URL != "" {
		t.Errorf("unsafe version accepted: %q %q", f.Versions.Stable, f.URLs.Stable.URL)
	}
	f = ParseTapFormula("class T < Formula\n  version \"2.1\"\n  url \"https://x/t-#{version}.tgz\"\n  sha256 \"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\"\nend\n", "acme/tools", "t", "Formula/t.rb")
	if f.URLs.Stable.URL != "https://x/t-2.1.tgz" || !strings.HasPrefix(f.URLs.Stable.Checksum, "cccc") {
		t.Errorf("formula url=%q sha=%q", f.URLs.Stable.URL, f.URLs.Stable.Checksum)
	}
}

func TestConfiguredTapsFromFilesTOMLForms(t *testing.T) {
	testutil.IsolateMise(t, "")
	cfg := filepath.Join(t.TempDir(), "mise.toml")
	testutil.Write(t, cfg, `[bootstrap.brew.taps]
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
