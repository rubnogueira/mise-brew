package homebrew

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/rubnogueira/mise-brew/internal/paths"
)

type CaskArtifact struct{ Kind, Name, Target string }

var ArtifactSingular = map[string]string{"Apps": "App", "Binaries": "Binary", "Pkgs": "Pkg", "Fonts": "Font", "Suites": "Suite", "Manpages": "Manpage"}

func CaskArtifacts(c *Cask) []CaskArtifact {
	var out []CaskArtifact
	appdir := CaskAppdir()
	// resolve expands ~, $HOME, $APPDIR and $HOMEBREW_PREFIX in a target and
	// roots a relative one in dir, as Homebrew does when it installs.
	resolve := func(dir, target string) string {
		t := expandCaskPath(target)
		if filepath.IsAbs(t) || strings.HasPrefix(t, "~") {
			return t
		}
		return filepath.Join(dir, t)
	}
	for _, raw := range c.Artifacts {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		for k, v := range m {
			var list []any
			if json.Unmarshal(v, &list) != nil || len(list) == 0 {
				continue
			}
			first, _ := list[0].(string)
			target := filepath.Base(first)
			if len(list) > 1 {
				if opts, ok := list[1].(map[string]any); ok {
					if t, ok := opts["target"].(string); ok && t != "" {
						target = t
					}
				}
			}
			switch k {
			case "app":
				out = append(out, CaskArtifact{Kind: "Apps", Name: first, Target: resolve(appdir, target)})
			case "binary":
				out = append(out, CaskArtifact{Kind: "Binaries", Name: first, Target: ExpandCaveats(resolve(filepath.Join(paths.BrewPrefix(), "bin"), target))})
			case "pkg":
				out = append(out, CaskArtifact{Kind: "Pkgs", Name: first})
			case "font":
				out = append(out, CaskArtifact{Kind: "Fonts", Name: first, Target: resolve(expandCaskPath("~/Library/Fonts"), filepath.Base(first))})
			case "suite":
				out = append(out, CaskArtifact{Kind: "Suites", Name: first, Target: resolve(appdir, target)})
			case "manpage":
				out = append(out, CaskArtifact{Kind: "Manpages", Name: first})
			}
		}
	}
	return out
}
