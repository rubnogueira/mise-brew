package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rubnogueira/mise-brew/internal/homebrew"
	"github.com/rubnogueira/mise-brew/internal/mise"
	"github.com/rubnogueira/mise-brew/internal/ui"
)

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
		taps := homebrew.ConfiguredTaps()
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
		ui.Odie("Invalid tap name: %q", o.Pos[0])
	}
	if tap == "homebrew/core" || tap == "homebrew/cask" {
		fmt.Printf("%s is provided by the formulae.brew.sh API; nothing to tap.\n", tap)
		return
	}
	a := append([]string{"bootstrap", "packages", "brew", "tap"}, mise.TapScopeArgs()...)
	if o.Has("dry-run") {
		a = append(a, "-n")
	}
	a = append(a, tap)
	if len(o.Pos) > 1 {
		a = append(a, o.Pos[1])
	} else if v, ok := o.Val("custom-remote"); ok && v != "" {
		a = append(a, v)
	}
	if code := mise.RunMise(a...); code != 0 {
		ui.Exit(code)
	}
}

func cmdUntap(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	if len(o.Pos) == 0 {
		ui.Odie("Invalid usage: This command requires at least 1 tap argument.")
	}
	a := append([]string{"bootstrap", "packages", "brew", "untap"}, mise.TapScopeArgs()...)
	if o.Has("dry-run") {
		a = append(a, "-n")
	}
	for _, t := range o.Pos {
		a = append(a, strings.ToLower(t))
	}
	if code := mise.RunMise(a...); code != 0 {
		ui.Exit(code)
	}
}

func cmdTapInfo(args []string) {
	o := parseArgs(args, nil, nil)
	taps := homebrew.ConfiguredTaps()
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
			ui.Odie("Invalid tap name: %q", t)
		}
		url, ok := taps[t]
		if url == "" {
			url = defaultTapURL(t)
		}
		var formulae []string
		for _, n := range homebrew.InstalledFormulae() {
			if r := homebrew.ReadReceipt(homebrew.KegPath(n, homebrew.CurrentVersion(n))); r != nil && strings.EqualFold(r.Source.Tap, t) {
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
			ui.Odie("Invalid usage: invalid JSON version: %s", v)
		}
		printJSON(out)
		return
	}
	if len(o.Pos) == 0 && !o.Has("installed") {
		fmt.Printf("%s\n", ui.Plural(len(out), "tap"))
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
		ui.Ohai("%s: %s", t.Name, state)
		fmt.Printf("%s (%s installed)\n", t.Remote, ui.Plural(len(t.Formulae), "formula"))
	}
}
