package cli

import (
	"strings"

	"github.com/rubnogueira/mise-brew/internal/ui"
)

// Opts is a Homebrew-style parsed command line: flags may appear anywhere.
type Opts struct {
	flags map[string]string
	order []string
	Pos   []string
}

// shortAliases maps single-letter flags to their long names (per command overrides allowed).
var commonShort = map[string]string{
	"v": "verbose",
	"q": "quiet",
	"d": "debug",
	"n": "dry-run",
	"f": "force",
	"h": "help",
	"1": "1",
	"l": "l",
	"t": "t",
	"r": "r",
	"s": "s",
	"g": "global",
}

// parseArgs parses args. valueFlags are long flags that consume the next argument
// when not given as --flag=value. short overrides commonShort for this command.
// currentCmd is the canonical command being run; its flag spec supplies short
// letters and value-taking flags in addition to the explicit arguments.
var currentCmd string

func parseArgs(args []string, valueFlagList []string, short map[string]string) Opts {
	o := Opts{flags: map[string]string{}}
	takesValue := map[string]bool{}
	for _, f := range append(valueFlagList, valueFlags(currentCmd)...) {
		takesValue[f] = true
	}
	// --json[=v2]-style flags bind a value only when it is attached.
	for _, f := range optionalValueFlags(currentCmd) {
		delete(takesValue, f)
	}
	merged := shortMap(currentCmd)
	for k, v := range short {
		merged[k] = v
	}
	short = merged
	set := func(k, v string) {
		if _, ok := o.flags[k]; !ok {
			o.order = append(o.order, k)
		}
		o.flags[k] = v
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			o.Pos = append(o.Pos, args[i+1:]...)
			return o
		case strings.HasPrefix(a, "--"):
			k, v, hasV := strings.Cut(a[2:], "=")
			if !hasV && takesValue[k] && i+1 < len(args) {
				v = args[i+1]
				i++
			}
			set(k, v)
		case strings.HasPrefix(a, "-") && len(a) > 1:
			// A short value option takes the rest of the cluster or the next arg (-n3, -n 3).
			if n, ok := short[string(a[1])]; ok && takesValue[n] {
				if len(a) > 2 {
					set(n, a[2:])
				} else if i+1 < len(args) {
					set(n, args[i+1])
					i++
				} else {
					set(n, "")
				}
				continue
			}
			for _, r := range a[1:] {
				s := string(r)
				name := s
				if short != nil {
					if n, ok := short[s]; ok {
						name = n
					} else if n, ok := commonShort[s]; ok {
						name = n
					}
				} else if n, ok := commonShort[s]; ok {
					name = n
				}
				set(name, "")
			}
		default:
			o.Pos = append(o.Pos, a)
		}
	}
	return o
}

func (o Opts) Has(names ...string) bool {
	for _, n := range names {
		if _, ok := o.flags[n]; ok {
			return true
		}
	}
	return false
}

func (o Opts) Val(names ...string) (string, bool) {
	for _, n := range names {
		if v, ok := o.flags[n]; ok {
			return v, true
		}
	}
	return "", false
}

// wantFormula / wantCask honour --formula/--formulae and --cask/--casks.
func (o Opts) onlyFormula() bool { return o.Has("formula", "formulae") && !o.Has("cask", "casks") }

func (o Opts) onlyCask() bool { return o.Has("cask", "casks") && !o.Has("formula", "formulae") }

// applyGlobal sets the process-wide verbosity flags.
func (o Opts) applyGlobal() {
	if o.Has("verbose") {
		ui.Verbose = true
	}
	if o.Has("quiet") {
		ui.Quiet = true
	}
	if o.Has("debug") {
		ui.Debug = true
	}
}

// withFlag returns a copy of o with flag k set; o itself is not modified.
func (o Opts) withFlag(k, v string) Opts {
	c := Opts{flags: make(map[string]string, len(o.flags)+1), order: append([]string(nil), o.order...), Pos: o.Pos}
	for kk, vv := range o.flags {
		c.flags[kk] = vv
	}
	if _, ok := c.flags[k]; !ok {
		c.order = append(c.order, k)
	}
	c.flags[k] = v
	return c
}
