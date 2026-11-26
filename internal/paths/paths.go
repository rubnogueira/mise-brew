package paths

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/rubnogueira/mise-brew/internal/ui"
)

// HomeDir returns $HOME, else the account's home directory from the user
// database. It returns "" (never a relative path) when neither is usable, so
// callers must not join "~" paths onto it without checking.
func HomeDir() string {
	if h, err := os.UserHomeDir(); err == nil && filepath.IsAbs(h) {
		return h
	}
	if h := UserHomeLookup(); filepath.IsAbs(h) {
		return h
	}
	return ""
}

// UserHomeLookup asks the user database for the home directory (a variable
// so tests can simulate an account without one).
var UserHomeLookup = func() string {
	if u, err := user.Current(); err == nil {
		return u.HomeDir
	}
	return ""
}

func defaultPrefix() string {
	switch runtime.GOOS {
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return "/opt/homebrew"
		}
		return "/usr/local"
	default:
		return "/home/linuxbrew/.linuxbrew"
	}
}

var PrefixCache string

func BrewPrefix() string {
	if PrefixCache != "" {
		return PrefixCache
	}
	p := os.Getenv("MISEBREW_PREFIX")
	if p == "" {
		p = defaultPrefix()
	}
	PrefixCache = filepath.Clean(p)
	return PrefixCache
}

func Cellar() string { return filepath.Join(BrewPrefix(), "Cellar") }

func Caskroom() string { return filepath.Join(BrewPrefix(), "Caskroom") }

func OptDir() string { return filepath.Join(BrewPrefix(), "opt") }

func LinkedDir() string {
	return filepath.Join(BrewPrefix(), "var", "homebrew", "linked")
}

func PinnedDir() string {
	return filepath.Join(BrewPrefix(), "var", "homebrew", "pinned")
}

func BrewRepository() string {
	if v := os.Getenv("MISEBREW_REPOSITORY"); v != "" {
		return v
	}
	if BrewPrefix() == "/usr/local" {
		return "/usr/local/Homebrew"
	}
	if runtime.GOOS == "linux" && BrewPrefix() == "/home/linuxbrew/.linuxbrew" {
		return "/home/linuxbrew/.linuxbrew/Homebrew"
	}
	return BrewPrefix()
}

func miseCacheDir() string {
	if v := os.Getenv("MISE_CACHE_DIR"); v != "" {
		return v
	}
	// mise honours XDG_CACHE_HOME on every platform, macOS included.
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "mise")
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(HomeDir(), "Library", "Caches", "mise")
	}
	return filepath.Join(HomeDir(), ".cache", "mise")
}

// BrewCacheDir is where mise keeps Homebrew bottles and API JSON.
func BrewCacheDir() string { return filepath.Join(miseCacheDir(), "system-brew") }

func ApiDir() string { return filepath.Join(BrewCacheDir(), "api") }

func BottlesDir() string { return filepath.Join(BrewCacheDir(), "bottles") }

var MiseBinCache string

func MiseBin() string {
	if MiseBinCache != "" {
		return MiseBinCache
	}
	if v := os.Getenv("MISEBREW_MISE"); v != "" {
		MiseBinCache = v
		return v
	}
	if p, err := exec.LookPath("mise"); err == nil {
		MiseBinCache = p
		return p
	}
	for _, p := range []string{
		filepath.Join(HomeDir(), ".local", "bin", "mise"),
		"/usr/local/bin/mise",
		"/opt/homebrew/bin/mise",
		"/usr/bin/mise",
	} {
		if FileExists(p) {
			MiseBinCache = p
			return p
		}
	}
	ui.Odie("mise was not found. Install it from https://mise.jdx.dev or set MISEBREW_MISE.")
	return ""
}

func FileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func IsDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func Tildify(p string) string {
	h := HomeDir()
	if h != "" && strings.HasPrefix(p, h+"/") {
		return "~" + p[len(h):]
	}
	return p
}
