package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// macOS major version <-> Homebrew codename (newest first).
var macosCodenames = []struct {
	major int
	name  string
}{
	{27, "golden_gate"}, {26, "tahoe"}, {15, "sequoia"}, {14, "sonoma"}, {13, "ventura"},
	{12, "monterey"}, {11, "big_sur"}, {10, "catalina"},
}

var (
	macosOnce  sync.Once
	macosMajor int
)

// currentMacOSMajor returns e.g. 27 (0 off macOS). MISEBREW_MACOS_VERSION overrides.
func currentMacOSMajor() int {
	macosOnce.Do(func() {
		if runtime.GOOS != "darwin" {
			return
		}
		v := os.Getenv("MISEBREW_MACOS_VERSION")
		if v == "" {
			if out, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
				v = strings.TrimSpace(string(out))
			}
		}
		major, _ := strconv.Atoi(strings.SplitN(v, ".", 2)[0])
		if major == 10 { // 10.15 -> catalina
			major = 10
		}
		macosMajor = major
	})
	return macosMajor
}

func macosCodename(major int) string {
	for _, c := range macosCodenames {
		if c.major == major {
			return c.name
		}
	}
	if major > macosCodenames[0].major {
		return macosCodenames[0].name
	}
	return ""
}

// macosMajorFor parses "ventura", "13", "13.0" or ":ventura".
func macosMajorFor(s string) int {
	s = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), ":")
	for _, c := range macosCodenames {
		if c.name == s {
			return c.major
		}
	}
	n, _ := strconv.Atoi(strings.SplitN(s, ".", 2)[0])
	return n
}

// platformTag is Homebrew's bottle/variation tag for this machine, e.g. arm64_golden_gate.
func platformTag() string {
	if v := os.Getenv("MISEBREW_BOTTLE_TAG"); v != "" {
		return v
	}
	if runtime.GOOS == "linux" {
		if runtime.GOARCH == "arm64" {
			return "arm64_linux"
		}
		return "x86_64_linux"
	}
	name := macosCodename(currentMacOSMajor())
	if runtime.GOARCH == "arm64" {
		return "arm64_" + name
	}
	return name
}

// bottleTagCandidates lists acceptable bottle tags, best first (newest compatible OS, then "all").
func bottleTagCandidates() []string {
	tag := platformTag()
	out := []string{tag}
	if runtime.GOOS == "darwin" {
		arm := runtime.GOARCH == "arm64"
		cur := currentMacOSMajor()
		for _, c := range macosCodenames {
			if c.major < cur {
				t := c.name
				if arm {
					t = "arm64_" + t
				}
				out = append(out, t)
			}
		}
	}
	return append(out, "all")
}

// applyVariations overlays the current platform's variation onto a formula record.
func applyVariations(raw json.RawMessage, f *Formula) {
	var v struct {
		Variations map[string]json.RawMessage `json:"variations"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Variations == nil {
		return
	}
	if over, ok := v.Variations[platformTag()]; ok {
		_ = json.Unmarshal(over, f)
	}
}

func applyVariationsMap(m map[string]any, keepVariations bool) {
	vars, _ := m["variations"].(map[string]any)
	if over, ok := vars[platformTag()].(map[string]any); ok {
		for k, v := range over {
			m[k] = v
		}
	}
	if !keepVariations {
		delete(m, "variations")
	}
}

// macosDepApplies reports whether uses_from_macos entry i is a real dependency here.
func macosDepApplies(f *Formula, i int) bool {
	if runtime.GOOS != "darwin" {
		return true
	}
	if i >= len(f.UsesFromMacosBounds) {
		return false
	}
	b := f.UsesFromMacosBounds[i]
	if since, ok := b["since"]; ok {
		return currentMacOSMajor() < macosMajorFor(since)
	}
	return false
}

// requirementText renders a requirement and whether it is satisfied.
func requirementText(name string, version *string) (string, bool) {
	ver := ""
	if version != nil {
		ver = *version
	}
	switch name {
	case "macos":
		if runtime.GOOS != "darwin" {
			return "macOS", false
		}
		if ver == "" {
			return "macOS", true
		}
		return "macOS >= " + ver, currentMacOSMajor() >= macosMajorFor(ver)
	case "maximum_macos":
		return "macOS <= " + ver, runtime.GOOS == "darwin" && currentMacOSMajor() <= macosMajorFor(ver)
	case "linux":
		return "Linux", runtime.GOOS == "linux"
	case "arch":
		want := ver
		have := map[string]string{"arm64": "arm64", "amd64": "x86_64"}[runtime.GOARCH]
		return want + " architecture", want == "" || want == have
	case "xcode":
		_, err := exec.LookPath("xcodebuild")
		ok := err == nil && fileExists("/Applications/Xcode.app")
		if ver != "" {
			return "Xcode >= " + ver, ok
		}
		return "Xcode", ok
	case "linuxkernel":
		return "Linux kernel", runtime.GOOS == "linux"
	}
	_, err := exec.LookPath(name)
	return name, err == nil
}
