package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
)

type brewfileEntry struct {
	Kind    string         `json:"kind"` // tap brew cask mas go cargo npm uv vscode whalebrew flatpak
	Name    string         `json:"name"`
	Args    []string       `json:"args"`    // extra positional args (tap URL)
	Options map[string]any `json:"options"` // keyword options (id:, link:, restart_service:, ...)
	Line    int            `json:"-"`
	Extra   string         `json:"-"` // tap URL
	ID      string         `json:"-"` // mas id
	Restart bool           `json:"-"`
}

func (e brewfileEntry) optString(k string) string {
	if v, ok := e.Options[k]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func (e brewfileEntry) optBool(k string) (bool, bool) {
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

// linkOption interprets `link:`: true, false or :overwrite.
func (e brewfileEntry) linkOption() (link, overwrite, ok bool) {
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

// restartService interprets `restart_service:`. As in Homebrew, any set value
// (true, :changed) restarts only when the formula was installed or upgraded;
// only :always restarts on every run. Returns "", "changed" or "always".
func (e brewfileEntry) restartService() string {
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

// startService interprets `start_service:`, which defaults to restart_service.
func (e brewfileEntry) startService() bool {
	v, ok := e.Options["start_service"]
	if !ok {
		return e.restartService() != ""
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

func (e brewfileEntry) optStrings(k string) []string {
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

var bundleCaskArgs map[string]any

func findRuby() string {
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

func parseBrewfileRuby(ruby, path string) ([]brewfileEntry, error) {
	entries, _, err := rubyBrewfile(ruby, path)
	return entries, err
}

// rubyBrewfile evaluates the Brewfile with Ruby; incomplete reports entries
// the DSL didn't understand and skipped.
func rubyBrewfile(ruby, path string) (entries []brewfileEntry, incomplete bool, err error) {
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
		cmd.Stdin = bytes.NewReader(stdinBrewfile())
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
		Entries  []brewfileEntry `json:"entries"`
		CaskArgs map[string]any  `json:"cask_args"`
		Skipped  int             `json:"skipped"`
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	dec.UseNumber() // keep App Store IDs exact
	if err := dec.Decode(&res); err != nil {
		return nil, false, err
	}
	bundleCaskArgs = res.CaskArgs
	for i := range res.Entries {
		normalizeEntry(&res.Entries[i])
	}
	return res.Entries, res.Skipped > 0, nil
}

func normalizeEntry(e *brewfileEntry) {
	if e.Options == nil {
		e.Options = map[string]any{}
	}
	if e.Kind == "cursor" || e.Kind == "antigravity" {
		e.Options["editor"] = e.Kind
		e.Kind = "vscode"
	}
	if e.Kind == "mas" {
		e.ID = e.optString("id")
	}
	if e.Kind == "tap" && len(e.Args) > 0 {
		e.Extra = e.Args[0]
	}
	e.Restart = e.restartService() != ""
}

var entryRe = regexp.MustCompile(`^\s*(tap|brew|cask|mas|vscode|cursor|antigravity|whalebrew|go|cargo|npm|uv|flatpak)\s*\(?\s*["']([^"']+)["']\s*(?:,\s*(.*?))?\s*\)?\s*$`)
var optRe = regexp.MustCompile(`(\w+):\s*("[^"]*"|'[^']*'|:\w+|true|false|-?\d+|\[[^\]]*\]|\{[^}]*\})`)
var condRe = regexp.MustCompile(`^(.*?)\s+(if|unless)\s+(.+)$`)
var blockIfRe = regexp.MustCompile(`^\s*(if|unless)\s+(.+?)(?:\s+then)?\s*$`)
var elsifRe = regexp.MustCompile(`^\s*elsif\s+(.+?)(?:\s+then)?\s*$`)

// openerRe matches lines that open a block closed by `end` other than if/unless.
var openerRe = regexp.MustCompile(`^\s*(begin|case|while|until|for|def|class|module)\b|\bdo\s*(\|[^|]*\|)?\s*$`)

func stripRubyComment(l string) string {
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

func evalCond(c string) (bool, bool) {
	c = strings.TrimSpace(c)
	neg := false
	if strings.HasPrefix(c, "!") {
		neg, c = true, strings.TrimSpace(c[1:])
	}
	var v bool
	switch c {
	case "OS.mac?", "OS.mac?()", "RUBY_PLATFORM.include?(\"darwin\")":
		v = runtime.GOOS == "darwin"
	case "OS.linux?", "OS.linux?()":
		v = runtime.GOOS == "linux"
	case "Hardware::CPU.arm?":
		v = runtime.GOARCH == "arm64"
	case "Hardware::CPU.intel?":
		v = runtime.GOARCH == "amd64"
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

// continuesLine reports whether a (comment-stripped) Ruby line continues on
// the next one: a trailing `,` or `\`, or an unclosed bracket.
func continuesLine(l string) bool {
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
		line := strings.TrimSpace(stripRubyComment(sc.Text()))
		if cur == nil {
			if line == "" {
				continue
			}
			cur = &brewfileLine{n: ln, text: line}
		} else if line != "" {
			cur.text = strings.TrimSpace(strings.TrimSuffix(cur.text, `\`)) + " " + line
		}
		if !continuesLine(cur.text) {
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
func parseBrewfile(r io.Reader, name string) []brewfileEntry {
	entries, _ := parseBrewfileFallback(r, name)
	return entries
}

// parseBrewfileFallback parses the common Brewfile subset without Ruby.
// incomplete reports that something couldn't be understood (skipped lines,
// unevaluable conditions or blocks), so the entries may not be the full set.
func parseBrewfileFallback(r io.Reader, name string) (out []brewfileEntry, incomplete bool) {
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
		opoo("%s:%d: can't evaluate `%s` without Ruby; assuming true", name, ln, what)
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
				opoo("%s:%d: can't evaluate `%s` without Ruby", name, ln, line)
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
			bundleCaskArgs = map[string]any{}
			for _, om := range optRe.FindAllStringSubmatch(line, -1) {
				bundleCaskArgs[om[1]] = parseOptValue(om[2])
			}
			continue
		}
		m := entryRe.FindStringSubmatch(line)
		if m == nil {
			opoo("%s:%d: skipping unsupported Brewfile line: %s", name, ln, line)
			incomplete = true
			continue
		}
		if !cond {
			continue
		}
		e := brewfileEntry{Kind: m[1], Name: m[2], Line: ln, Options: map[string]any{}}
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

func brewfilePath(o Opts) string {
	if f, ok := o.Val("file"); ok && f != "" {
		return f
	}
	if o.Has("global") {
		if f := os.Getenv("HOMEBREW_BUNDLE_FILE_GLOBAL"); f != "" {
			return f
		}
		if x := os.Getenv("XDG_CONFIG_HOME"); x != "" && fileExists(filepath.Join(x, "homebrew", "Brewfile")) {
			return filepath.Join(x, "homebrew", "Brewfile")
		}
		return filepath.Join(homeDir(), ".Brewfile")
	}
	if f := os.Getenv("HOMEBREW_BUNDLE_FILE"); f != "" {
		return f
	}
	return "Brewfile"
}

// readBrewfile parses the Brewfile. incomplete reports that some of it was
// skipped, so it must not be used to decide what to remove.
func readBrewfile(o Opts) (entries []brewfileEntry, incomplete bool) {
	p := brewfilePath(o)
	if p != "-" && !fileExists(p) {
		odie("No Brewfile found: %s", p)
	}
	rubyFailed := false
	if ruby := findRuby(); ruby != "" && os.Getenv("MISEBREW_BUNDLE_NO_RUBY") == "" {
		entries, incomplete, err := rubyBrewfile(ruby, p)
		if err == nil {
			return entries, incomplete
		}
		opoo("%v; falling back to the built-in Brewfile parser", err)
		// Ruby could not evaluate it, so the fallback parse is a best effort:
		// never let a destructive cleanup trust it.
		rubyFailed = true
	}
	if p == "-" {
		entries, incomplete := parseBrewfileFallback(bytes.NewReader(stdinBrewfile()), "stdin")
		return entries, incomplete || rubyFailed
	}
	f, err := os.Open(p)
	if err != nil {
		odie("No Brewfile found: %s", p)
	}
	defer f.Close()
	entries, incomplete = parseBrewfileFallback(f, p)
	return entries, incomplete || rubyFailed
}

var (
	stdinBrewfileOnce sync.Once
	stdinBrewfileData []byte
)

// stdinBrewfile returns a Brewfile given as --file=-. Stdin can only be read
// once, but install --cleanup parses the Brewfile twice (and Ruby may fail
// after consuming it), so the bytes are read a single time and reused.
func stdinBrewfile() []byte {
	stdinBrewfileOnce.Do(func() {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			odie("reading Brewfile from stdin: %v", err)
		}
		stdinBrewfileData = data
	})
	return stdinBrewfileData
}

// refuseIncomplete stops a cleanup that would act on a partially parsed Brewfile.
func refuseIncomplete(what string) {
	odie("Refusing to run `brew bundle %s`: parts of the Brewfile could not be evaluated (see the warnings above), so packages it declares could be removed.\nInstall Ruby >= 2.6 (or set MISEBREW_RUBY) so the Brewfile can be evaluated in full, or simplify it.", what)
}

// --- keys -------------------------------------------------------------------

func toolKey(e brewfileEntry) string {
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

func packageKey(e brewfileEntry) string {
	switch e.Kind {
	case "brew":
		return "brew:" + stripCoreTap(e.Name)
	case "cask":
		return "brew-cask:" + stripCoreTap(e.Name)
	case "mas":
		if e.ID != "" {
			return "mas:" + e.ID
		}
	case "flatpak":
		return "flatpak:" + e.Name
	}
	return ""
}

func editorCLI(e brewfileEntry) string {
	ed := e.optString("editor")
	if ed == "" {
		ed = os.Getenv("HOMEBREW_BUNDLE_VSCODE_EDITOR")
	}
	if ed != "" {
		return ed
	}
	for _, c := range []string{"code", "codium", "cursor", "code-insiders"} {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	return "code"
}

func vscodeExtensions(cli string) map[string]bool {
	out := map[string]bool{}
	b, err := exec.Command(cli, "--list-extensions").Output()
	if err != nil {
		return out
	}
	for _, l := range strings.Fields(string(b)) {
		out[strings.ToLower(l)] = true
	}
	return out
}

func runShell(script string) error {
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// --- commands ---------------------------------------------------------------

func cmdBundle(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	sub := "install"
	pos := o.Pos
	if len(pos) > 0 {
		sub, pos = pos[0], pos[1:]
	}
	if o.Has("install") && sub != "install" {
		bundleInstall(o)
	}
	switch sub {
	case "install", "upgrade":
		if sub == "upgrade" {
			o.flags["upgrade"] = ""
		}
		bundleInstall(o)
	case "check":
		if !bundleCheck(o, true) {
			exit(1)
		}
	case "list":
		bundleList(o)
	case "dump":
		bundleDump(o)
	case "cleanup":
		bundleCleanup(o)
	case "exec", "sh", "env":
		bundleExec(o, sub, pos)
	case "add":
		bundleEdit(o, pos, true)
	case "remove":
		bundleEdit(o, pos, false)
	case "edit":
		openInEditor(brewfilePath(o))
	default:
		odie("Unknown `brew bundle` subcommand: %s", sub)
	}
}

func bundleNoUpgrade(o Opts) bool {
	if o.Has("upgrade") {
		return false
	}
	return o.Has("no-upgrade") || envSet("HOMEBREW_BUNDLE_NO_UPGRADE")
}

func bundleInstall(o Opts) {
	entries, incomplete := readBrewfile(o)
	if incomplete && o.Has("cleanup") && !o.Has("dry-run") {
		refuseIncomplete("install --cleanup")
	}
	dry := o.Has("dry-run")
	files := configFiles()
	taps := configuredTaps()
	for _, e := range entries {
		if e.Kind != "tap" {
			continue
		}
		t := strings.ToLower(e.Name)
		if _, ok := taps[t]; ok || strings.HasPrefix(t, "homebrew/") {
			continue
		}
		a := append([]string{"bootstrap", "packages", "brew", "tap"}, tapScopeArgs()...)
		if dry {
			a = append(a, "-n")
		}
		a = append(a, t)
		if e.Extra != "" {
			a = append(a, e.Extra)
		}
		if code := runMise(a...); code != 0 {
			exit(code)
		}
	}
	if !dry { // conflicting formulae are unlinked first, as brew bundle does
		func() {
			defer lockPrefix()()
			for _, e := range entries {
				for _, c := range e.optStrings("conflicts_with") {
					if r := resolveInstalledFormula(c); r != "" && isKegLinked(r) {
						fmt.Printf("Unlinking %s (conflicts with %s)\n", r, e.Name)
						unlinkKeg(r, linkedVersion(r), false)
					}
				}
			}
		}()
	}
	// Group packages by the cask appdir they need (mise reads it from the env).
	type group struct {
		env        []string
		use, apply []string
	}
	groups := map[string]*group{}
	globalAppdir := ""
	if v, ok := bundleCaskArgs["appdir"]; ok {
		globalAppdir = expandUserPath(fmt.Sprint(v))
	}
	var tools, upgrades []string
	for _, e := range entries {
		if e.Kind == "brew" {
			if a := e.optStrings("args"); len(a) > 0 {
				opoo("brew \"%s\": args %v need a source build and were ignored (mise installs bottles).", e.Name, a)
			}
		}
		k := packageKey(e)
		if k == "" {
			if tk := toolKey(e); tk != "" {
				tools = append(tools, tk)
			}
			continue
		}
		appdir := globalAppdir
		if m, ok := e.Options["args"].(map[string]any); ok && e.Kind == "cask" {
			if v, ok := m["appdir"]; ok {
				appdir = expandUserPath(fmt.Sprint(v))
			}
		}
		g := groups[appdir]
		if g == nil {
			g = &group{}
			if appdir != "" {
				g.env = []string{"MISE_BREW_CASK_OPT_APPDIR=" + appdir}
			}
			groups[appdir] = g
		}
		if !bundleNoUpgrade(o) {
			if r := resolveInstalledFormula(e.Name); e.Kind == "brew" && r != "" {
				if len(outdatedFormulae([]string{r}, false)) > 0 && !isPinned(r) {
					upgrades = append(upgrades, formulaKey(r))
				}
			} else if t := resolveInstalledCask(e.Name); e.Kind == "cask" && t != "" {
				gr := greedy{}
				if b, _ := e.optBool("greedy"); b {
					gr = greedy{true, true}
				}
				if len(outdatedCasks([]string{t}, gr)) > 0 {
					upgrades = append(upgrades, "brew-cask:"+t)
				}
			}
		}
		if !recordEnabled() || isDeclared(k, files) {
			g.apply = append(g.apply, k)
		} else {
			g.use = append(g.use, k)
		}
	}
	before, beforeCasks := snapshotKegs(), snapshotCasks()
	r := &miseRun{}
	runKeys := func(env []string, a []string, keys []string) {
		if len(keys) == 0 {
			return
		}
		if dry {
			a = append(a, "-n")
		}
		r.extraEnv = env
		if code := runMiseBrewStyle(r, append(a, keys...)...); code != 0 {
			exit(code)
		}
	}
	var dirs []string
	for d := range groups {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		g := groups[d]
		use := append([]string{"bootstrap", "packages", "use"}, scopeArgs()...)
		runKeys(g.env, append(use, "-y"), g.use)
		runKeys(g.env, []string{"bootstrap", "packages", "apply", "-y"}, g.apply)
	}
	runKeys(nil, []string{"bootstrap", "packages", "upgrade", "-y"}, upgrades)
	if len(tools) > 0 {
		a := append([]string{"use"}, scopeArgs()...)
		if dry {
			a = append(a, "--dry-run")
		}
		if code := runMise(append(a, tools...)...); code != 0 {
			exit(code)
		}
	}
	// Editor extensions and whalebrew images are installed by their own CLIs.
	for _, e := range entries {
		switch e.Kind {
		case "vscode":
			cli := editorCLI(e)
			if vscodeExtensions(cli)[strings.ToLower(e.Name)] {
				continue
			}
			fmt.Printf("Installing %s extension %s\n", cli, e.Name)
			if !dry {
				cmd := exec.Command(cli, "--install-extension", e.Name)
				cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
				if err := cmd.Run(); err != nil {
					onoe("Installing %s has failed!", e.Name)
					exit(1)
				}
			}
		case "whalebrew":
			if _, err := exec.LookPath("whalebrew"); err != nil {
				opoo("Skipping whalebrew \"%s\": whalebrew is not installed", e.Name)
				continue
			}
			if !dry {
				cmd := exec.Command("whalebrew", "install", e.Name)
				cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
				_ = cmd.Run()
			}
		}
	}
	if dry {
		return
	}
	changed := map[string]bool{}
	for _, n := range changedFormulae(before) {
		changed[n] = true
	}
	newCasks := changedCasks(beforeCasks)
	failures := 0
	for _, e := range entries {
		if e.Kind == "cask" {
			// As in Homebrew, only casks this run installed or upgraded.
			if pi := e.optString("postinstall"); pi != "" && newCasks[resolveInstalledCask(e.Name)] {
				if err := runShell(pi); err != nil {
					onoe("postinstall for %s failed: %v", e.Name, err)
					failures++
				}
			}
			continue
		}
		if e.Kind != "brew" {
			continue
		}
		n := resolveInstalledFormula(e.Name)
		if n == "" {
			continue
		}
		if l, overwrite, ok := e.linkOption(); ok {
			if !bundleLink(n, l, overwrite) {
				failures++
			}
		}
		if s := findService(n); s != nil {
			switch rs := e.restartService(); {
			case rs == "always" || (rs == "changed" && changed[n]):
				if s.state().Loaded {
					serviceStop(s, false)
				}
				serviceStart(s, "", "")
			case e.startService() && !s.state().Running:
				serviceStart(s, "", "")
			}
		}
		if pi := e.optString("postinstall"); pi != "" && changed[n] {
			if err := runShell(pi); err != nil {
				onoe("postinstall for %s failed: %v", n, err)
				failures++
			}
		}
	}
	installCleanup(changedFormulae(before))
	if failures > 0 {
		onoe("`brew bundle` failed! %d Brewfile %s failed to install", failures, map[bool]string{true: "dependency", false: "dependencies"}[failures == 1])
		exit(1)
	}
	if !quiet {
		n := 0
		for _, e := range entries {
			if e.Kind != "tap" {
				n++
			}
		}
		oh1("`brew bundle` complete! %d Brewfile %s now installed.", n, map[bool]string{true: "dependency", false: "dependencies"}[n == 1])
	}
	if o.Has("cleanup") {
		o.flags["force"] = ""
		bundleCleanup(o)
	}
}

// bundleLink applies a Brewfile `link:` option; false when linking failed.
func bundleLink(n string, link, overwrite bool) bool {
	defer lockPrefix()()
	if link && !isKegLinked(n) {
		_, conflicts, err := linkKeg(n, currentVersion(n), overwrite, false)
		if err != nil {
			onoe("Linking %s failed: %v", n, err)
			return false
		}
		if len(conflicts) > 0 {
			c := conflicts[0]
			onoe("Could not symlink %s\nTarget %s\nalready exists. Use `link: :overwrite` in the Brewfile to overwrite it.", c.rel, c.dst)
			return false
		}
	} else if !link && isKegLinked(n) {
		unlinkKeg(n, linkedVersion(n), false)
	}
	return true
}

// snapshotCasks records the installed version directories of every cask.
func snapshotCasks() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, t := range installedCasks() {
		out[t] = map[string]bool{}
		for _, v := range caskVersions(t) {
			out[t][v] = true
		}
	}
	return out
}

// changedCasks lists casks that gained a version since the snapshot.
func changedCasks(before map[string]map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, t := range installedCasks() {
		for _, v := range caskVersions(t) {
			if !before[t][v] {
				out[t] = true
				break
			}
		}
	}
	return out
}

func bundleCheck(o Opts, report bool) bool {
	entries, _ := readBrewfile(o)
	taps := configuredTaps()
	var missing []string
	var masState map[string]bool
	exts := map[string]map[string]bool{}
	checkUpgrade := !bundleNoUpgrade(o)
	for _, e := range entries {
		switch e.Kind {
		case "tap":
			t := strings.ToLower(e.Name)
			if _, ok := taps[t]; !ok && !strings.HasPrefix(t, "homebrew/") {
				missing = append(missing, fmt.Sprintf("Tap %s needs to be tapped.", e.Name))
			}
		case "brew":
			r := resolveInstalledFormula(e.Name)
			if r == "" {
				missing = append(missing, fmt.Sprintf("Formula %s needs to be installed.", e.Name))
			} else if checkUpgrade && len(outdatedFormulae([]string{r}, false)) > 0 && !isPinned(r) {
				missing = append(missing, fmt.Sprintf("Formula %s needs to be upgraded.", e.Name))
			}
		case "cask":
			t := resolveInstalledCask(e.Name)
			if t == "" {
				missing = append(missing, fmt.Sprintf("Cask %s needs to be installed.", e.Name))
			} else if checkUpgrade && len(outdatedCasks([]string{t}, greedy{})) > 0 {
				missing = append(missing, fmt.Sprintf("Cask %s needs to be upgraded.", e.Name))
			}
		case "mas":
			if masState == nil {
				masState = map[string]bool{}
				for _, p := range packagesStatus()["mas"].Packages {
					masState[p.Package] = p.State == "installed"
				}
			}
			if !masState[e.ID] {
				missing = append(missing, fmt.Sprintf("App %s needs to be installed or updated.", e.Name))
			}
		case "vscode":
			cli := editorCLI(e)
			if exts[cli] == nil {
				exts[cli] = vscodeExtensions(cli)
			}
			if !exts[cli][strings.ToLower(e.Name)] {
				missing = append(missing, fmt.Sprintf("VSCode Extension %s needs to be installed.", e.Name))
			}
		default:
			if k := toolKey(e); k != "" {
				if _, err := miseOutput("where", k); err != nil {
					missing = append(missing, fmt.Sprintf("%s package %s needs to be installed.", e.Kind, e.Name))
				}
			}
		}
	}
	if !report {
		return len(missing) == 0
	}
	if len(missing) == 0 {
		fmt.Println("The Brewfile's dependencies are satisfied.")
		return true
	}
	fmt.Println("brew bundle can't satisfy your Brewfile's dependencies.")
	if !verbose && len(missing) > 1 {
		missing = missing[:1]
	}
	for _, m := range missing {
		fmt.Println("→ " + m)
	}
	fmt.Println("Satisfy missing dependencies with `brew bundle install`.")
	return false
}

var bundleKindFlags = map[string]string{"formula": "brew", "formulae": "brew", "brews": "brew", "cask": "cask", "casks": "cask", "tap": "tap", "taps": "tap", "mas": "mas", "vscode": "vscode", "go": "go", "cargo": "cargo", "npm": "npm", "uv": "uv", "flatpak": "flatpak", "whalebrew": "whalebrew"}

func bundleList(o Opts) {
	entries, _ := readBrewfile(o)
	kinds := map[string]bool{}
	for flag, kind := range bundleKindFlags {
		if o.Has(flag) {
			kinds[kind] = true
		}
	}
	if len(kinds) == 0 && !o.Has("all") {
		kinds["brew"] = true
	}
	for _, e := range entries {
		if o.Has("all") || kinds[e.Kind] {
			fmt.Println(e.Name)
		}
	}
}

func bundleDump(o Opts) {
	var b strings.Builder
	describe := o.Has("describe")
	only := map[string]bool{}
	for flag, kind := range bundleKindFlags {
		if o.Has(flag) {
			only[kind] = true
		}
	}
	want := func(kind string) bool {
		if o.Has("no-"+kind) || (kind == "brew" && o.Has("no-brew")) {
			return false
		}
		return len(only) == 0 || only[kind]
	}
	comment := func(d string) {
		if describe && d != "" {
			fmt.Fprintf(&b, "# %s\n", d)
		}
	}
	if want("tap") {
		taps := configuredTaps()
		var names []string
		for t := range taps {
			names = append(names, t)
		}
		sort.Strings(names)
		for _, t := range names {
			if u := taps[t]; u != "" && u != defaultTapURL(t) && u != defaultTapURL(t)+".git" {
				fmt.Fprintf(&b, "tap %q, %q\n", t, u)
			} else {
				fmt.Fprintf(&b, "tap %q\n", t)
			}
		}
	}
	if want("brew") {
		for _, n := range sortedKeys(declaredFullNames("brew")) {
			if f := lookupFormula(n); f != nil {
				comment(f.Desc)
			}
			line := fmt.Sprintf("brew %q", n)
			if r := resolveInstalledFormula(n); r != "" {
				if !o.Has("no-restart") {
					if s := findService(r); s != nil && s.state().Running {
						line += ", restart_service: :changed"
					}
				}
				if f := lookupFormula(r); f != nil && !f.KegOnly && !isKegLinked(r) {
					line += ", link: false"
				} else if f != nil && f.KegOnly && isKegLinked(r) {
					line += ", link: true"
				}
			}
			b.WriteString(line + "\n")
		}
	}
	if want("cask") {
		for _, n := range sortedKeys(declaredFullNames("brew-cask")) {
			if c := lookupCask(n); c != nil {
				comment(c.DescString())
			}
			fmt.Fprintf(&b, "cask %q\n", n)
		}
	}
	if want("mas") {
		names := masNames()
		for _, id := range sortedKeys(declaredSet("mas")) {
			n := names[id]
			if n == "" {
				n = id
			}
			fmt.Fprintf(&b, "mas %q, id: %s\n", n, id)
		}
	}
	if want("vscode") {
		for _, e := range sortedKeys(vscodeExtensions(editorCLI(brewfileEntry{}))) {
			fmt.Fprintf(&b, "vscode %q\n", e)
		}
	}
	for _, kind := range []string{"go", "cargo", "npm", "uv"} {
		if !want(kind) {
			continue
		}
		for _, t := range globalMiseTools(map[string]string{"go": "go", "cargo": "cargo", "npm": "npm", "uv": "pipx"}[kind]) {
			fmt.Fprintf(&b, "%s %q\n", kind, t)
		}
	}
	if want("flatpak") {
		for _, n := range sortedKeys(declaredSet("flatpak")) {
			fmt.Fprintf(&b, "flatpak %q\n", n)
		}
	}
	path := brewfilePath(o)
	if path == "-" {
		fmt.Print(b.String())
		return
	}
	if fileExists(path) && !o.Has("force") {
		odie("%s already exists. Use `--force` to overwrite.", path)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		odie("%v", err)
	}
}

// declaredFullNames is declaredSet with tap-qualified names (owner/tap/name)
// restored, so a dump reinstalls from the same tap.
func declaredFullNames(manager string) map[string]bool {
	names := declaredSet(manager)
	for _, p := range packagesStatus()[manager].Packages {
		if strings.Contains(p.Package, "/") && (p.DesiredState == "" || p.DesiredState == "present") {
			delete(names, baseName(p.Package))
			names[p.Package] = true
		}
	}
	return names
}

// masNames maps App Store IDs to names via `mas list`.
func masNames() map[string]string {
	out := map[string]string{}
	b, err := exec.Command("mas", "list").Output()
	if err != nil {
		return out
	}
	re := regexp.MustCompile(`^\s*(\d+)\s+(.+?)\s+\(`)
	for _, l := range strings.Split(string(b), "\n") {
		if m := re.FindStringSubmatch(l); m != nil {
			out[m[1]] = m[2]
		}
	}
	return out
}

// globalMiseTools lists tools of a backend in the global mise config.
func globalMiseTools(backend string) []string {
	b, err := miseOutput("ls", "--global", "--json")
	if err != nil {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	var out []string
	for k := range m {
		if strings.HasPrefix(k, backend+":") {
			out = append(out, strings.TrimPrefix(k, backend+":"))
		}
	}
	sort.Strings(out)
	return out
}

// tapOfFullName returns owner/tap of a tap-qualified owner/tap/name.
func tapOfFullName(name string) string {
	if strings.Count(name, "/") != 2 {
		return ""
	}
	return strings.ToLower(name[:strings.LastIndex(name, "/")])
}

// miseGlobalConfigDir is mise's global config directory.
func miseGlobalConfigDir() string {
	if d := os.Getenv("MISE_CONFIG_DIR"); d != "" {
		return d
	}
	x := os.Getenv("XDG_CONFIG_HOME")
	if x == "" {
		x = filepath.Join(homeDir(), ".config")
	}
	return filepath.Join(x, "mise")
}

func pathWithin(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// isGlobalMiseConfig reports whether f is mise's global config (or the file
// MISEBREW_CONFIG points mise-brew at), as opposed to a project's config.
func isGlobalMiseConfig(f string) bool {
	cands := []string{f}
	if r, err := filepath.EvalSymlinks(f); err == nil {
		cands = append(cands, r)
	}
	for _, c := range cands {
		if v := os.Getenv("MISEBREW_CONFIG"); v != "" && v != "global" && v != "local" && filepath.Clean(c) == filepath.Clean(v) {
			return true
		}
		if v := os.Getenv("MISE_GLOBAL_CONFIG_FILE"); v != "" && filepath.Clean(c) == filepath.Clean(v) {
			return true
		}
		dir := miseGlobalConfigDir()
		if pathWithin(c, dir) {
			return true
		}
		if r, err := filepath.EvalSymlinks(dir); err == nil && pathWithin(c, r) {
			return true
		}
	}
	return false
}

// projectDeclarations lists non-global config files declaring any of keys.
// `brew bundle cleanup` leaves those packages alone: uninstalling would
// undeclare them from a project's config as well.
func projectDeclarations(keys, files []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range keys {
		for _, d := range findDeclarations(k, files) {
			if !seen[d.File] && !isGlobalMiseConfig(d.File) {
				seen[d.File] = true
				out = append(out, d.File)
			}
		}
	}
	return out
}

// skipProjectDeclared drops (with a warning) items declared in a project config.
func skipProjectDeclared(items []string, keys func(string) []string, files []string) []string {
	var out []string
	for _, n := range items {
		if where := projectDeclarations(keys(n), files); len(where) > 0 {
			for i := range where {
				where[i] = tildify(where[i])
			}
			opoo("Not uninstalling %s: it is declared in %s; remove it there first.", n, strings.Join(where, ", "))
			continue
		}
		out = append(out, n)
	}
	return out
}

// tapsToUntap mirrors Homebrew's taps_to_untap: current taps not named by a
// tap entry, by a tap-qualified brew/cask entry (owner/tap/name), or by the
// receipt of a kept, installed formula.
func tapsToUntap(entries []brewfileEntry, keepF map[string]bool, current map[string]string) []string {
	keep := map[string]bool{"homebrew/core": true}
	for _, e := range entries {
		switch e.Kind {
		case "tap":
			keep[strings.ToLower(e.Name)] = true
		case "brew":
			if t := tapOfFullName(stripCoreTap(e.Name)); t != "" {
				keep[t] = true
			}
		case "cask":
			full := e.optString("full_name")
			if full == "" {
				full = e.Name
			}
			if t := tapOfFullName(stripCoreTap(full)); t != "" {
				keep[t] = true
			}
		}
	}
	for n := range keepF {
		if v := currentVersion(n); v != "" {
			if r := readReceipt(kegPath(n, v)); r != nil && r.Source.Tap != "" {
				keep[strings.ToLower(r.Source.Tap)] = true
			}
		}
	}
	var out []string
	for t := range current {
		if !keep[strings.ToLower(t)] {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func bundleCleanup(o Opts) {
	entries, incomplete := readBrewfile(o)
	if incomplete {
		if o.Has("force") {
			refuseIncomplete("cleanup --force")
		}
		opoo("Parts of the Brewfile could not be evaluated; this list may include packages it declares.")
	}
	keepF, keepC, keepExt := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, e := range entries {
		switch e.Kind {
		case "brew":
			n := baseName(stripCoreTap(e.Name))
			if f := lookupFormula(n); f != nil {
				n = f.Name
			}
			keepF[n] = true
			if r := resolveInstalledFormula(e.Name); r != "" {
				keepF[r] = true
			}
		case "cask":
			keepC[baseName(stripCoreTap(e.Name))] = true
			// Renamed casks stay installed under their old token, as check sees them.
			if t := resolveInstalledCask(e.Name); t != "" {
				keepC[t] = true
			}
		case "vscode":
			keepExt[strings.ToLower(e.Name)] = true
		}
	}
	deps := installedRuntimeDeps()
	var walk func(string)
	walk = func(n string) {
		for _, d := range deps[n] {
			if !keepF[d] {
				keepF[d] = true
				walk(d)
			}
		}
	}
	for n := range keepF {
		walk(n)
	}
	for _, c := range installedCasks() {
		if keepC[c] {
			if cc := lookupCask(c); cc != nil {
				for _, d := range cc.DependsOn.Formula {
					keepF[d] = true
					walk(d)
				}
			}
		}
	}
	var rmF, rmC, rmExt []string
	for _, n := range installedFormulae() {
		if !keepF[n] {
			rmF = append(rmF, n)
		}
	}
	for _, c := range installedCasks() {
		if !keepC[c] {
			rmC = append(rmC, c)
		}
	}
	rmT := tapsToUntap(entries, keepF, configuredTapURLs())
	if len(rmF)+len(rmC) > 0 {
		files := configFiles()
		rmF = skipProjectDeclared(rmF, func(n string) []string { return []string{"brew:" + n, formulaKey(n)} }, files)
		rmC = skipProjectDeclared(rmC, func(t string) []string { return []string{"brew-cask:" + t} }, files)
	}
	if len(keepExt) > 0 || o.Has("vscode") {
		for e := range vscodeExtensions(editorCLI(brewfileEntry{})) {
			if !keepExt[e] {
				rmExt = append(rmExt, e)
			}
		}
	}
	sort.Strings(rmT)
	sort.Strings(rmExt)
	if len(rmF)+len(rmC)+len(rmT)+len(rmExt) == 0 {
		return
	}
	if !o.Has("force") {
		for _, grp := range []struct {
			what  string
			items []string
		}{{"casks", rmC}, {"formulae", rmF}, {"taps", rmT}, {"VSCode extensions", rmExt}} {
			if len(grp.items) > 0 {
				fmt.Printf("Would uninstall %s:\n", grp.what)
				putsColumns(grp.items)
			}
		}
		fmt.Println("Run `brew bundle cleanup --force` to make these changes.")
		return
	}
	if len(rmC) > 0 {
		a := []string{"--cask"}
		if o.Has("zap") {
			a = append(a, "--zap")
		}
		cmdUninstall(append(a, rmC...))
	}
	if len(rmF) > 0 {
		cmdUninstall(append([]string{"--formula", "--ignore-dependencies"}, rmF...))
	}
	if len(rmT) > 0 {
		cmdUntap(rmT)
	}
	for _, e := range rmExt {
		cli := editorCLI(brewfileEntry{})
		_ = exec.Command(cli, "--uninstall-extension", e).Run()
		fmt.Printf("Uninstalled %s extension %s\n", cli, e)
	}
}

func bundleExec(o Opts, sub string, cmdArgs []string) {
	if o.Has("check") && !bundleCheck(o, false) {
		bundleCheck(o, true)
		exit(1)
	}
	entries, _ := readBrewfile(o)
	var formulae []string
	for _, e := range entries {
		if e.Kind == "brew" {
			formulae = append(formulae, baseName(e.Name))
		}
	}
	envPairs := buildEnv(formulae, false)
	if sub == "env" {
		for _, kv := range envPairs {
			fmt.Printf("export %s=%q\n", kv[0], kv[1])
		}
		return
	}
	for i, kv := range envPairs {
		if kv[0] == "PATH" {
			envPairs[i][1] = kv[1] + ":" + os.Getenv("PATH")
		}
	}
	env := mergeEnv(os.Environ(), envPairs)
	if o.Has("no-secrets") {
		var clean []string
		for _, e := range env {
			k, _, _ := strings.Cut(e, "=")
			uk := strings.ToUpper(k)
			if strings.Contains(uk, "TOKEN") || strings.Contains(uk, "SECRET") || strings.Contains(uk, "PASSWORD") || strings.HasSuffix(uk, "_KEY") {
				continue
			}
			clean = append(clean, e)
		}
		env = clean
	}
	if sub == "sh" {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		cmdArgs = []string{sh}
	}
	if len(cmdArgs) == 0 {
		odie("Invalid usage: `brew bundle exec` needs a command to run.")
	}
	path, err := exec.LookPath(cmdArgs[0])
	if err != nil {
		odie("%v", err)
	}
	if o.Has("services") {
		runWithServices(formulae, path, cmdArgs, env)
	}
	if err := syscall.Exec(path, cmdArgs, env); err != nil {
		odie("%v", err)
	}
}

// runWithServices runs a command with the Brewfile's services temporarily
// started (`brew bundle exec --services`). Only services that weren't already
// running or registered are started, and exactly those are stopped afterwards,
// keeping any registered service file, as Homebrew's `keep: true`.
func runWithServices(formulae []string, path string, cmdArgs, env []string) {
	type temp struct {
		s           *service
		createdFile bool
	}
	var started []temp
	// The child gets Ctrl-C itself; the parent must survive it (and a stray
	// SIGTERM) to stop the services.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	// Deferred so the services are stopped even when starting one fails.
	defer func() {
		for _, t := range started {
			serviceStop(t.s, true)
			if t.createdFile {
				_ = os.Remove(t.s.installedPath())
				_ = runQuiet("systemctl", systemctlArgs("daemon-reload")...)
			}
		}
	}()
	for _, f := range formulae {
		s := findService(f)
		if s == nil {
			continue
		}
		if st := s.state(); st.Running || st.Loaded || fileExists(s.installedPath()) {
			continue
		}
		serviceRun(s)
		// serviceRun registers the unit on Linux; remove it again afterwards.
		started = append(started, temp{s, runtime.GOOS != "darwin" && fileExists(s.installedPath())})
	}
	cmd := exec.Command(path, cmdArgs[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		onoe("%v", err)
		exit(127)
	}
	// Forward SIGTERM sent to brew alone (kill, timeout(1), a supervisor) to
	// the child; remember the last signal so brew exits like it was killed by it
	// once the services are stopped.
	var got os.Signal
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var err error
	for done := false; !done; {
		select {
		case sig := <-sigs:
			got = sig
			// A terminal Ctrl-C already reached the child through the
			// process group; sending it twice makes many tools force-quit.
			if sig != os.Interrupt {
				_ = cmd.Process.Signal(sig)
			}
		case err = <-waited:
			done = true
		}
	}
	if s, ok := got.(syscall.Signal); ok && got != nil {
		exit(128 + int(s))
	}
	exit(exitCode(err))
}

// bundleEntryRe matches entries of the given kinds and names anywhere in a
// line, like Homebrew's remover: `brew "x"`, `brew("x", ...)`, `... if OS.mac?`.
func bundleEntryRe(kinds, names []string) *regexp.Regexp {
	q := func(xs []string) string {
		var out []string
		for _, x := range xs {
			out = append(out, regexp.QuoteMeta(x))
		}
		return strings.Join(out, "|")
	}
	return regexp.MustCompile(`\b(` + q(kinds) + `)\s*\(?\s*["'](` + q(names) + `)["']`)
}

// editBrewfileLines adds (or removes) entries of kinds named names. Removing
// drops every matching line plus the continuation lines of multi-line entries.
func editBrewfileLines(lines []string, kinds, names []string, add bool) []string {
	if add {
		for _, n := range names {
			re := bundleEntryRe(kinds[:1], []string{n})
			found := false
			for _, l := range lines {
				if re.MatchString(stripRubyComment(l)) {
					found = true
					break
				}
			}
			if !found {
				lines = append(lines, fmt.Sprintf("%s %q", kinds[0], n))
			}
		}
		return lines
	}
	re := bundleEntryRe(kinds, names)
	var kept []string
	for i := 0; i < len(lines); i++ {
		code := stripRubyComment(lines[i])
		if !re.MatchString(code) {
			kept = append(kept, lines[i])
			continue
		}
		for continuesLine(code) && i+1 < len(lines) {
			i++
			code = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(code), `\`)) + " " + strings.TrimSpace(stripRubyComment(lines[i]))
		}
	}
	return kept
}

func bundleEdit(o Opts, names []string, add bool) {
	if len(names) == 0 {
		odie("Invalid usage: `brew bundle %s` needs at least one name.", map[bool]string{true: "add", false: "remove"}[add])
	}
	var flags, kinds []string
	for f := range bundleKindFlags {
		flags = append(flags, f)
	}
	sort.Strings(flags)
	seen := map[string]bool{}
	for _, flag := range flags {
		if k := bundleKindFlags[flag]; o.Has(flag) && !seen[k] {
			seen[k] = true
			kinds = append(kinds, k)
		}
	}
	if len(kinds) == 0 {
		if add {
			kinds = []string{"brew"}
		} else {
			// Without a type flag, remove matching entries of every kind.
			kinds = []string{"tap", "brew", "cask", "mas", "vscode", "cursor", "antigravity", "whalebrew", "go", "cargo", "npm", "uv", "flatpak"}
		}
	}
	path := brewfilePath(o)
	data, err := os.ReadFile(path)
	if err != nil && (!add || !os.IsNotExist(err)) {
		odie("No Brewfile found: %s", path)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	lines = editBrewfileLines(lines, kinds, names, add)
	out := strings.Join(lines, "\n")
	if out != "" {
		out += "\n"
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		odie("%v", err)
	}
}
