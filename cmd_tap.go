package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// configuredTaps returns tap -> URL ("" when implied) from config and installed kegs.
// configuredTapsFromFiles reads [bootstrap.brew.taps] from every mise config file.
func configuredTapsFromFiles() map[string]string {
	taps := map[string]string{}
	for _, f := range configFiles() {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		table := ""
		for _, line := range strings.Split(string(data), "\n") {
			if m := tableHeaderRe.FindStringSubmatch(line); m != nil {
				table = normalizeTOMLKey(m[1])
				continue
			}
			key, val, ok := keyOfLine(line)
			if !ok {
				continue
			}
			full := key
			if table != "" {
				full = table + "." + key
			}
			if strings.HasPrefix(full, "bootstrap.brew.taps.") {
				tap := strings.TrimPrefix(full, "bootstrap.brew.taps.")
				taps[strings.ToLower(tap)] = strings.Trim(strings.TrimSpace(stripComment(val)), `"'`)
			}
		}
	}
	return taps
}

// configuredTaps returns tap -> URL ("" when implied) from config, declared
// tap-qualified packages and installed kegs.
func configuredTaps() map[string]string {
	taps := map[string]string{}
	for k, v := range configuredTapURLs() {
		taps[k] = v
	}
	for _, m := range []string{"brew", "brew-cask"} {
		for _, p := range packagesStatus()[m].Packages {
			if strings.Count(p.Package, "/") == 2 {
				tap := strings.ToLower(p.Package[:strings.LastIndex(p.Package, "/")])
				if _, ok := taps[tap]; !ok {
					taps[tap] = ""
				}
			}
		}
	}
	for _, n := range installedFormulae() {
		if r := readReceipt(kegPath(n, currentVersion(n))); r != nil && r.Source.Tap != "" {
			t := strings.ToLower(r.Source.Tap)
			if t != "homebrew/core" && t != "homebrew/cask" {
				if _, ok := taps[t]; !ok {
					taps[t] = ""
				}
			}
		}
	}
	return taps
}

func defaultTapURL(tap string) string {
	parts := strings.SplitN(tap, "/", 2)
	if len(parts) != 2 {
		return ""
	}
	return "https://github.com/" + parts[0] + "/homebrew-" + strings.TrimPrefix(parts[1], "homebrew-")
}

func cmdTap(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	if len(o.Pos) == 0 {
		taps := configuredTaps()
		var names []string
		for t := range taps {
			names = append(names, t)
		}
		sort.Strings(names)
		for _, t := range names {
			fmt.Println(t)
		}
		return
	}
	tap := strings.ToLower(o.Pos[0])
	if strings.Count(tap, "/") != 1 {
		odie("Invalid tap name: %q", o.Pos[0])
	}
	if tap == "homebrew/core" || tap == "homebrew/cask" {
		fmt.Printf("%s is provided by the formulae.brew.sh API; nothing to tap.\n", tap)
		return
	}
	a := append([]string{"bootstrap", "packages", "brew", "tap"}, tapScopeArgs()...)
	if o.Has("dry-run") {
		a = append(a, "-n")
	}
	a = append(a, tap)
	if len(o.Pos) > 1 {
		a = append(a, o.Pos[1])
	} else if v, ok := o.Val("custom-remote"); ok && v != "" {
		a = append(a, v)
	}
	if code := runMise(a...); code != 0 {
		exit(code)
	}
}

func cmdUntap(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	if len(o.Pos) == 0 {
		odie("Invalid usage: This command requires at least 1 tap argument.")
	}
	a := append([]string{"bootstrap", "packages", "brew", "untap"}, tapScopeArgs()...)
	if o.Has("dry-run") {
		a = append(a, "-n")
	}
	for _, t := range o.Pos {
		a = append(a, strings.ToLower(t))
	}
	if code := runMise(a...); code != 0 {
		exit(code)
	}
}

func cmdTapInfo(args []string) {
	o := parseArgs(args, nil, nil)
	taps := configuredTaps()
	names := o.Pos
	if len(names) == 0 || o.Has("installed") {
		names = nil
		for t := range taps {
			names = append(names, t)
		}
		sort.Strings(names)
	}
	type info struct {
		Name      string   `json:"name"`
		User      string   `json:"user"`
		Repo      string   `json:"repo"`
		Path      string   `json:"path"`
		Installed bool     `json:"installed"`
		Official  bool     `json:"official"`
		Remote    string   `json:"remote"`
		Formulae  []string `json:"formula_names"`
	}
	var out []info
	for _, t := range names {
		t = strings.ToLower(t)
		parts := strings.SplitN(t, "/", 2)
		if len(parts) != 2 {
			odie("Invalid tap name: %q", t)
		}
		url, ok := taps[t]
		if url == "" {
			url = defaultTapURL(t)
		}
		var formulae []string
		for _, n := range installedFormulae() {
			if r := readReceipt(kegPath(n, currentVersion(n))); r != nil && strings.EqualFold(r.Source.Tap, t) {
				formulae = append(formulae, t+"/"+n)
			}
		}
		if formulae == nil {
			formulae = []string{}
		}
		out = append(out, info{t, parts[0], strings.TrimPrefix(parts[1], "homebrew-"), "", ok, parts[0] == "homebrew", url, formulae})
	}
	if v, ok := o.Val("json"); ok {
		// A bare --json means v1, Homebrew's default and only version.
		if v != "" && v != "v1" {
			odie("Invalid usage: invalid JSON version: %s", v)
		}
		printJSON(out)
		return
	}
	if len(o.Pos) == 0 && !o.Has("installed") {
		fmt.Printf("%s\n", plural(len(out), "tap"))
		return
	}
	for i, t := range out {
		if i > 0 {
			fmt.Println()
		}
		state := "Not installed"
		if t.Installed {
			state = "Installed"
		}
		ohai("%s: %s", t.Name, state)
		fmt.Printf("%s (%s installed)\n", t.Remote, plural(len(t.Formulae), "formula"))
	}
}
