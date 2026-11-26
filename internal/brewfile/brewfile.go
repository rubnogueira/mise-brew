package brewfile

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/rubnogueira/mise-brew/internal/homebrew"
	"github.com/rubnogueira/mise-brew/internal/ui"
)

type BrewfileEntry struct {
	Kind    string         `json:"kind"` // tap brew cask mas go cargo npm uv vscode whalebrew flatpak
	Name    string         `json:"name"`
	Args    []string       `json:"args"`    // extra positional args (tap URL)
	Options map[string]any `json:"options"` // keyword options (id:, link:, restart_service:, ...)
	Line    int            `json:"-"`
	Extra   string         `json:"-"` // tap URL
	ID      string         `json:"-"` // mas id
	Restart bool           `json:"-"`
}

func (e BrewfileEntry) OptString(k string) string {
	if v, ok := e.Options[k]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func (e BrewfileEntry) OptBool(k string) (bool, bool) {
	v, ok := e.Options[k]
	if !ok {
		return false, false
	}
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		return t == "true" || t == "always" || t == "changed", true
	}
	return false, true
}

// LinkOption interprets `link:`: true, false or :overwrite.
func (e BrewfileEntry) LinkOption() (link, overwrite, ok bool) {
	switch v := e.Options["link"].(type) {
	case bool:
		return v, false, true
	case string:
		switch v {
		case "overwrite":
			return true, true, true
		case "true":
			return true, false, true
		case "false":
			return false, false, true
		}
	}
	return false, false, false
}

// RestartService interprets `restart_service:`. As in Homebrew, any set value
// (true, :changed) restarts only when the formula was installed or upgraded;
// only :always restarts on every run. Returns "", "changed" or "always".
func (e BrewfileEntry) RestartService() string {
	switch v := e.Options["restart_service"].(type) {
	case bool:
		if v {
			return "changed"
		}
	case string:
		switch v {
		case "", "false":
		case "always":
			return "always"
		default:
			return "changed"
		}
	}
	return ""
}

// StartService interprets `start_service:`, which defaults to restart_service.
func (e BrewfileEntry) StartService() bool {
	v, ok := e.Options["start_service"]
	if !ok {
		return e.RestartService() != ""
	}
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != "" && t != "false"
	}
	return true
}

func (e BrewfileEntry) OptStrings(k string) []string {
	switch v := e.Options[k].(type) {
	case []any:
		var out []string
		for _, x := range v {
			out = append(out, fmt.Sprint(x))
		}
		return out
	case string:
		return []string{v}
	}
	return nil
}

// --- parsing ----------------------------------------------------------------

// rubyDSL evaluates a Brewfile with Ruby, like Homebrew does, and writes JSON
// to the file named by its second argument (stdout stays the Brewfile's own).
// It must run on Ruby 2.6, the /usr/bin/ruby macOS ships: no endless methods.
const rubyDSL = `# frozen_string_literal: true
require "json"
module OS
  def self.mac?; RUBY_PLATFORM.include?("darwin"); end
  def self.linux?; RUBY_PLATFORM.include?("linux"); end
end
module Hardware
  module CPU
    def self.arm?; RUBY_PLATFORM.match?(/arm64|aarch64/); end
    def self.intel?; !arm?; end
    def self.arch; arm? ? :arm64 : :x86_64; end
  end
end
class MiseBrewBundleDSL
  attr_reader :entries, :cask_arguments, :skipped
  def initialize
    @entries = []
    @cask_arguments = {}
    @skipped = 0
  end
  %w[tap brew cask mas vscode cursor whalebrew go cargo npm uv flatpak antigravity].each do |kind|
    define_method(kind) do |name, *rest, **opts|
      @entries << { kind: kind, name: name.to_s, args: rest.map(&:to_s), options: jsonable(opts) }
    end
  end
  def cask_args(**opts)
    @cask_arguments.merge!(jsonable(opts))
  end
  def jsonable(v)
    case v
    when Hash then v.each_with_object({}) { |(k, x), h| h[k.to_s] = jsonable(x) }
    when Array then v.map { |x| jsonable(x) }
    when Symbol then v.to_s
    else v
    end
  end
  def method_missing(name, *_args, **_opts)
    @skipped += 1
    warn "Warning: skipping unsupported Brewfile entry: #{name}"
  end
  def respond_to_missing?(*)
    true
  end
end
dsl = MiseBrewBundleDSL.new
path = ARGV.fetch(0)
dsl.instance_eval(path == "-" ? $stdin.read : File.read(path), path)
File.write(ARGV.fetch(1), JSON.generate({ entries: dsl.entries, cask_args: dsl.cask_arguments, skipped: dsl.skipped }))
`

var BundleCaskArgs map[string]any

func FindRuby() string {
	if v := os.Getenv("MISEBREW_RUBY"); v != "" {
		return v
	}
	if p, err := exec.LookPath("ruby"); err == nil {
		// rubyDSL needs Ruby >= 2.6 (macOS's /usr/bin/ruby).
		if out, err := exec.Command(p, "-e", "print((RUBY_VERSION.split('.').map(&:to_i) <=> [2, 6]) >= 0)").Output(); err == nil && string(out) == "true" {
			return p
		}
	}
	return ""
}

func parseBrewfileRuby(ruby, path string) ([]BrewfileEntry, error) {
	entries, _, err := RubyBrewfile(ruby, path)
	return entries, err
}

// RubyBrewfile evaluates the Brewfile with Ruby; incomplete reports entries
// the DSL didn't understand and skipped.
func RubyBrewfile(ruby, path string) (entries []BrewfileEntry, incomplete bool, err error) {
	dir, err := os.MkdirTemp("", "mise-brew-bundle-")
	if err != nil {
		return nil, false, err
	}
	defer os.RemoveAll(dir)
	shim := filepath.Join(dir, "dsl.rb")
	if err := os.WriteFile(shim, []byte(rubyDSL), 0o644); err != nil {
		return nil, false, err
	}
	outFile := filepath.Join(dir, "out.json")
	abs := path
	if path != "-" {
		abs, _ = filepath.Abs(path)
	}
	cmd := exec.Command(ruby, "--disable-gems", shim, abs, outFile)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if path == "-" {
		cmd.Stdin = bytes.NewReader(StdinBrewfile())
	}
	if path != "-" {
		cmd.Dir = filepath.Dir(abs)
	}
	if err := cmd.Run(); err != nil {
		return nil, false, fmt.Errorf("evaluating %s: %v", path, err)
	}
	out, err := os.ReadFile(outFile)
	if err != nil {
		return nil, false, fmt.Errorf("evaluating %s: %v", path, err)
	}
	var res struct {
		Entries  []BrewfileEntry `json:"entries"`
		CaskArgs map[string]any  `json:"cask_args"`
		Skipped  int             `json:"skipped"`
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	dec.UseNumber() // keep App Store IDs exact
	if err := dec.Decode(&res); err != nil {
		return nil, false, err
	}
	BundleCaskArgs = res.CaskArgs
	for i := range res.Entries {
		normalizeEntry(&res.Entries[i])
	}
	return res.Entries, res.Skipped > 0, nil
}

func normalizeEntry(e *BrewfileEntry) {
	if e.Options == nil {
		e.Options = map[string]any{}
	}
	if e.Kind == "cursor" || e.Kind == "antigravity" {
		e.Options["editor"] = e.Kind
		e.Kind = "vscode"
	}
	if e.Kind == "mas" {
		e.ID = e.OptString("id")
	}
	if e.Kind == "tap" && len(e.Args) > 0 {
		e.Extra = e.Args[0]
	}
	e.Restart = e.RestartService() != ""
}

var entryRe = regexp.MustCompile(`^\s*(tap|brew|cask|mas|vscode|cursor|antigravity|whalebrew|go|cargo|npm|uv|flatpak)\s*\(?\s*["']([^"']+)["']\s*(?:,\s*(.*?))?\s*\)?\s*$`)

var optRe = regexp.MustCompile(`(\w+):\s*("[^"]*"|'[^']*'|:\w+|true|false|-?\d+|\[[^\]]*\]|\{[^}]*\})`)

var condRe = regexp.MustCompile(`^(.*?)\s+(if|unless)\s+(.+)$`)

var blockIfRe = regexp.MustCompile(`^\s*(if|unless)\s+(.+?)(?:\s+then)?\s*$`)

var elsifRe = regexp.MustCompile(`^\s*elsif\s+(.+?)(?:\s+then)?\s*$`)

// openerRe matches lines that open a block closed by `end` other than if/unless.
var openerRe = regexp.MustCompile(`^\s*(begin|case|while|until|for|def|class|module)\b|\bdo\s*(\|[^|]*\|)?\s*$`)

func StripRubyComment(l string) string {
	var q rune
	for i, r := range l {
		switch {
		case q != 0:
			if r == q {
				q = 0
			}
		case r == '"' || r == '\'':
			q = r
		case r == '#':
			return l[:i]
		}
	}
	return l
}

// hostOS and hostArch are the platform Brewfile conditions are evaluated for
// (variables so tests can pin them).
var hostOS, hostArch = runtime.GOOS, runtime.GOARCH

func evalCond(c string) (bool, bool) {
	c = strings.TrimSpace(c)
	neg := false
	if strings.HasPrefix(c, "!") {
		neg, c = true, strings.TrimSpace(c[1:])
	}
	var v bool
	switch c {
	case "OS.mac?", "OS.mac?()", "RUBY_PLATFORM.include?(\"darwin\")":
		v = hostOS == "darwin"
	case "OS.linux?", "OS.linux?()":
		v = hostOS == "linux"
	case "Hardware::CPU.arm?":
		v = hostArch == "arm64"
	case "Hardware::CPU.intel?":
		v = hostArch == "amd64"
	case "true":
		v = true
	case "false":
		v = false
	default:
		return true, false
	}
	return v != neg, true
}

func parseOptValue(s string) any {
	switch {
	case s == "true":
		return true
	case s == "false":
		return false
	case strings.HasPrefix(s, ":"):
		return s[1:]
	case strings.HasPrefix(s, "{"):
		m := map[string]any{}
		for _, om := range optRe.FindAllStringSubmatch(strings.Trim(s, "{}"), -1) {
			m[om[1]] = parseOptValue(om[2])
		}
		return m
	case strings.HasPrefix(s, "["):
		var out []any
		for _, p := range strings.Split(strings.Trim(s, "[]"), ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, parseOptValue(p))
			}
		}
		return out
	case strings.HasPrefix(s, `"`) || strings.HasPrefix(s, "'"):
		return strings.Trim(s, `"'`)
	}
	return s
}

// ContinuesLine reports whether a (comment-stripped) Ruby line continues on
// the next one: a trailing `,` or `\`, or an unclosed bracket.
func ContinuesLine(l string) bool {
	l = strings.TrimSpace(l)
	if strings.HasSuffix(l, ",") || strings.HasSuffix(l, `\`) {
		return true
	}
	depth := 0
	var q rune
	for _, r := range l {
		switch {
		case q != 0:
			if r == q {
				q = 0
			}
		case r == '"' || r == '\'':
			q = r
		case r == '(' || r == '[' || r == '{':
			depth++
		case r == ')' || r == ']' || r == '}':
			depth--
		}
	}
	return depth > 0
}

type brewfileLine struct {
	n    int // first physical line
	text string
}

// brewfileLines strips comments and joins continuation lines.
func brewfileLines(r io.Reader) []brewfileLine {
	var out []brewfileLine
	var cur *brewfileLine
	sc := bufio.NewScanner(r)
	ln := 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(StripRubyComment(sc.Text()))
		if cur == nil {
			if line == "" {
				continue
			}
			cur = &brewfileLine{n: ln, text: line}
		} else if line != "" {
			cur.text = strings.TrimSpace(strings.TrimSuffix(cur.text, `\`)) + " " + line
		}
		if !ContinuesLine(cur.text) {
			out = append(out, *cur)
			cur = nil
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// parseBrewfile is the fallback parser used when Ruby is unavailable.
func parseBrewfile(r io.Reader, name string) []BrewfileEntry {
	entries, _ := ParseBrewfileFallback(r, name)
	return entries
}

// ParseBrewfileFallback parses the common Brewfile subset without Ruby.
// incomplete reports that something couldn't be understood (skipped lines,
// unevaluable conditions or blocks), so the entries may not be the full set.
func ParseBrewfileFallback(r io.Reader, name string) (out []BrewfileEntry, incomplete bool) {
	type block struct {
		cond  bool // the current branch is taken
		taken bool // some branch of this if/unless was taken
		isIf  bool
	}
	var stack []block
	active := func() bool {
		for _, s := range stack {
			if !s.cond {
				return false
			}
		}
		return true
	}
	cantEval := func(ln int, what string) {
		ui.Opoo("%s:%d: can't evaluate `%s` without Ruby; assuming true", name, ln, what)
		incomplete = true
	}
	for _, l := range brewfileLines(r) {
		line, ln := l.text, l.n
		if m := blockIfRe.FindStringSubmatch(line); m != nil && !entryRe.MatchString(line) {
			v, ok := evalCond(m[2])
			if !ok && active() {
				cantEval(ln, m[2])
			}
			if m[1] == "unless" {
				v = !v
			}
			stack = append(stack, block{v, v, true})
			continue
		}
		if m := elsifRe.FindStringSubmatch(line); m != nil && len(stack) > 0 && stack[len(stack)-1].isIf {
			top := &stack[len(stack)-1]
			top.cond = false
			if !top.taken {
				stack = stack[:len(stack)-1]
				v, ok := evalCond(m[1])
				if !ok && active() {
					cantEval(ln, m[1])
				}
				stack = append(stack, block{v, v, true})
			}
			continue
		}
		if line == "else" && len(stack) > 0 {
			if top := &stack[len(stack)-1]; top.isIf {
				top.cond, top.taken = !top.taken, true
			}
			continue
		}
		if line == "end" && len(stack) > 0 {
			stack = stack[:len(stack)-1]
			continue
		}
		if m := openerRe.FindStringSubmatch(line); m != nil && !entryRe.MatchString(line) {
			if active() {
				ui.Opoo("%s:%d: can't evaluate `%s` without Ruby", name, ln, line)
				incomplete = true
			}
			// Method bodies don't run where they're defined.
			stack = append(stack, block{cond: m[1] != "def"})
			continue
		}
		if !active() {
			continue
		}
		cond := true
		if m := condRe.FindStringSubmatch(line); m != nil && entryRe.MatchString(m[1]) {
			v, ok := evalCond(m[3])
			if !ok {
				cantEval(ln, m[3])
			}
			if m[2] == "unless" {
				v = !v
			}
			cond, line = v, m[1]
		}
		if strings.HasPrefix(line, "cask_args") {
			BundleCaskArgs = map[string]any{}
			for _, om := range optRe.FindAllStringSubmatch(line, -1) {
				BundleCaskArgs[om[1]] = parseOptValue(om[2])
			}
			continue
		}
		m := entryRe.FindStringSubmatch(line)
		if m == nil {
			ui.Opoo("%s:%d: skipping unsupported Brewfile line: %s", name, ln, line)
			incomplete = true
			continue
		}
		if !cond {
			continue
		}
		e := BrewfileEntry{Kind: m[1], Name: m[2], Line: ln, Options: map[string]any{}}
		rest := m[3]
		if e.Kind == "tap" && strings.HasPrefix(strings.TrimSpace(rest), `"`) {
			e.Args = []string{strings.Trim(strings.SplitN(strings.TrimSpace(rest), ",", 2)[0], `"' `)}
		}
		for _, om := range optRe.FindAllStringSubmatch(rest, -1) {
			e.Options[om[1]] = parseOptValue(om[2])
		}
		normalizeEntry(&e)
		out = append(out, e)
	}
	return out, incomplete
}

var (
	StdinBrewfileOnce sync.Once
	StdinBrewfileData []byte
)

// StdinBrewfile returns a Brewfile given as --file=-. Stdin can only be read
// once, but install --cleanup parses the Brewfile twice (and Ruby may fail
// after consuming it), so the bytes are read a single time and reused.
func StdinBrewfile() []byte {
	StdinBrewfileOnce.Do(func() {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			ui.Odie("reading Brewfile from stdin: %v", err)
		}
		StdinBrewfileData = data
	})
	return StdinBrewfileData
}

// --- keys -------------------------------------------------------------------

func ToolKey(e BrewfileEntry) string {
	switch e.Kind {
	case "go":
		return "go:" + e.Name
	case "cargo":
		return "cargo:" + e.Name
	case "npm":
		return "npm:" + e.Name
	case "uv":
		return "pipx:" + e.Name
	}
	return ""
}

func PackageKey(e BrewfileEntry) string {
	switch e.Kind {
	case "brew":
		return "brew:" + homebrew.StripCoreTap(e.Name)
	case "cask":
		return "brew-cask:" + homebrew.StripCoreTap(e.Name)
	case "mas":
		if e.ID != "" {
			return "mas:" + e.ID
		}
	case "flatpak":
		return "flatpak:" + e.Name
	}
	return ""
}
