package homebrew

import (
	"github.com/BurntSushi/toml"
	"os"
	"strings"

	"github.com/rubnogueira/mise-brew/internal/mise"
	"github.com/rubnogueira/mise-brew/internal/ui"
)

// configuredTaps returns tap -> URL ("" when implied) from config and installed kegs.
// configuredTapsFromFiles reads [bootstrap.brew.taps] from every mise config file.
func configuredTapsFromFiles() map[string]string {
	taps := map[string]string{}
	for _, f := range mise.ConfigFiles() {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		// Read-only, so use a real TOML parser: it handles every table form,
		// arrays of tables and multi-line values.
		var doc map[string]any
		if _, err := toml.Decode(string(data), &doc); err != nil {
			ui.Odebug("skipping taps in %s: %v", f, err)
			continue
		}
		bootstrap, _ := doc["bootstrap"].(map[string]any)
		brew, _ := bootstrap["brew"].(map[string]any)
		configured, _ := brew["taps"].(map[string]any)
		for tap, v := range configured {
			url := ""
			switch v := v.(type) {
			case string:
				url = v
			case map[string]any:
				url, _ = v["url"].(string)
			}
			taps[strings.ToLower(tap)] = url
		}
	}
	return taps
}

// ConfiguredTaps returns tap -> URL ("" when implied) from config, declared
// tap-qualified packages and installed kegs.
func ConfiguredTaps() map[string]string {
	taps := map[string]string{}
	for k, v := range ConfiguredTapURLs() {
		taps[k] = v
	}
	for _, m := range []string{"brew", "brew-cask"} {
		for _, p := range mise.PackagesStatus()[m].Packages {
			if strings.Count(p.Package, "/") == 2 {
				tap := strings.ToLower(p.Package[:strings.LastIndex(p.Package, "/")])
				if _, ok := taps[tap]; !ok {
					taps[tap] = ""
				}
			}
		}
	}
	for _, n := range InstalledFormulae() {
		if r := ReadReceipt(KegPath(n, CurrentVersion(n))); r != nil && r.Source.Tap != "" {
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
