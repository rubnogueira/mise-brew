package brewfile

import (
	"runtime"
	"strings"
	"testing"
)

// Findings 6 and 7.
func TestBrewfileOptionInterpretation(t *testing.T) {
	es := parseBrewfile(strings.NewReader(`brew "a", link: :overwrite
brew "b", link: false
brew "c", restart_service: true
brew "d", restart_service: :always
brew "e", restart_service: :changed, start_service: false
brew "f", start_service: true
brew "g"
`), "Brewfile")
	got := map[string]BrewfileEntry{}
	for _, e := range es {
		got[e.Name] = e
	}
	cases := []struct {
		name                 string
		link, overwrite, set bool
		restart              string
		start                bool
	}{
		{"a", true, true, true, "", false},
		{"b", false, false, true, "", false},
		{"c", false, false, false, "changed", true},
		{"d", false, false, false, "always", true},
		{"e", false, false, false, "changed", false},
		{"f", false, false, false, "", true},
		{"g", false, false, false, "", false},
	}
	for _, c := range cases {
		e := got[c.name]
		l, ow, ok := e.LinkOption()
		if l != c.link || ow != c.overwrite || ok != c.set {
			t.Errorf("%s: linkOption = %v %v %v", c.name, l, ow, ok)
		}
		if r := e.RestartService(); r != c.restart {
			t.Errorf("%s: restartService = %q want %q", c.name, r, c.restart)
		}
		if s := e.StartService(); s != c.start {
			t.Errorf("%s: startService = %v", c.name, s)
		}
	}
	bools := BrewfileEntry{Options: map[string]any{"link": false, "restart_service": true}}
	if l, _, ok := bools.LinkOption(); l || !ok || bools.RestartService() != "changed" {
		t.Error("bool options from Ruby JSON")
	}
}

// Finding 11.
func TestFallbackParserBlocks(t *testing.T) {
	pinMacOS(t)
	src := `if OS.linux?
  brew "linux-a"
elsif OS.mac?
  brew "mac-a"
else
  brew "other-a"
end
if false
  brew "never"
elsif true
  brew "elsif-taken"
elsif true
  brew "second-elsif"
else
  brew "else-after-taken"
end
if OS.mac?
  [1].each do |x|
    puts x
  end
  brew "mac-after-block"
end
brew "multi",
  link: false,
  restart_service: :always
brew "args", args: [
  "with-foo",
]
brew "tail" # comment, with comma,
`
	es, inc := ParseBrewfileFallback(strings.NewReader(src), "Brewfile")
	if !inc {
		t.Error("do block should mark the parse incomplete")
	}
	got := map[string]BrewfileEntry{}
	for _, e := range es {
		got[e.Name] = e
	}
	mac := runtime.GOOS == "darwin"
	want := map[string]bool{
		"linux-a": runtime.GOOS == "linux", "mac-a": mac, "other-a": !mac && runtime.GOOS != "linux",
		"never": false, "elsif-taken": true, "second-elsif": false, "else-after-taken": false,
		"mac-after-block": mac, "multi": true, "args": true, "tail": true,
	}
	for n, w := range want {
		if _, ok := got[n]; ok != w {
			t.Errorf("%s present=%v want %v (got %v)", n, ok, w, es)
		}
	}
	m := got["multi"]
	if l, _, ok := m.LinkOption(); !ok || l || m.RestartService() != "always" {
		t.Errorf("multi options = %v", m.Options)
	}
	if a := got["args"].OptStrings("args"); len(a) != 1 || a[0] != "with-foo" {
		t.Errorf("args = %v", got["args"].Options)
	}
	if m.Line != 23 {
		t.Errorf("multi line = %d", m.Line)
	}
}
