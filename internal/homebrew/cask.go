package homebrew

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/rubnogueira/mise-brew/internal/paths"
)

// ExpandUserPath expands a leading ~. When the home directory is unknown the
// path is returned unchanged (still relative), never rooted at /.
func ExpandUserPath(p string) string {
	s, _ := expandUserPathOK(p)
	return s
}

// expandUserPathOK is expandUserPath that also reports whether a ~ could be expanded.
func expandUserPathOK(p string) (string, bool) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		h := paths.HomeDir()
		if h == "" {
			return p, false
		}
		return filepath.Join(h, strings.TrimPrefix(p, "~")), true
	}
	return p, true
}

// CaskAppdir is where cask apps live (mise's appdir override, else /Applications).
func CaskAppdir() string {
	if appdir := os.Getenv("MISE_BREW_CASK_OPT_APPDIR"); appdir != "" {
		return appdir
	}
	return "/Applications"
}

// expandCaskPath resolves the placeholders cask stanzas use.
func expandCaskPath(p string) string {
	s, _ := ExpandCaskPathOK(p)
	return s
}

// ExpandCaskPathOK is expandCaskPath that reports false (and leaves p alone)
// when p needs the home directory and it is unknown.
func ExpandCaskPathOK(p string) (string, bool) {
	h := paths.HomeDir()
	if h == "" && strings.Contains(strings.ReplaceAll(p, "$HOMEBREW_PREFIX", ""), "$HOME") {
		return p, false
	}
	appdir := CaskAppdir()
	p = strings.NewReplacer(
		"$HOMEBREW_PREFIX", paths.BrewPrefix(), "{{HOMEBREW_PREFIX}}", paths.BrewPrefix(),
		"$APPDIR", appdir, "{{appdir}}", appdir, "$HOME", h,
	).Replace(p)
	return expandUserPathOK(p)
}
