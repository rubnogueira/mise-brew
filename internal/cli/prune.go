package cli

import (
	"regexp"
	"strings"

	"github.com/rubnogueira/mise-brew/internal/homebrew"
	"github.com/rubnogueira/mise-brew/internal/mise"
)

// declaredSet returns declared (present) package names for a manager.
func declaredSet(manager string) map[string]bool {
	out := map[string]bool{}
	for _, p := range mise.PackagesStatus()[manager].Packages {
		if p.DesiredState == "" || p.DesiredState == "present" {
			out[homebrew.BaseName(p.Package)] = true
		}
	}
	return out
}

// pruneCandidates parses `mise bootstrap packages prune --dry-run` for a manager.
func pruneCandidates(manager string, force bool) ([]string, string) {
	args := []string{"bootstrap", "packages", "prune", "-m", manager, "--dry-run"}
	if force {
		args = append(args, "--force")
	}
	out, _ := mise.MiseCombined(args...)
	var names []string
	re := regexp.MustCompile(`(?m)^\s*(?:mise\s+)?(?:` + regexp.QuoteMeta(manager) + `:\s*)?remove\s+` + regexp.QuoteMeta(manager) + `:(\S+)`)
	for _, m := range re.FindAllStringSubmatch(string(out), -1) {
		n := m[1]
		// strip trailing @version (names may contain @ themselves, e.g. openssl@3@3.6.4)
		if i := strings.LastIndex(n, "@"); i > 0 && !homebrew.IsFormulaInstalled(n) && !homebrew.IsCaskInstalled(n) {
			if rest := n[i+1:]; len(rest) > 0 && (rest[0] >= '0' && rest[0] <= '9' || rest == "latest") {
				n = n[:i]
			}
		}
		names = append(names, n)
	}
	return names, string(out)
}
