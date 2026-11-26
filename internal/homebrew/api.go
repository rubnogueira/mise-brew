package homebrew

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rubnogueira/mise-brew/internal/paths"
	"github.com/rubnogueira/mise-brew/internal/ui"
)

// Formula mirrors the fields of the formulae.brew.sh API that mise-brew uses.
type Formula struct {
	Name              string   `json:"name"`
	FullName          string   `json:"full_name"`
	Tap               string   `json:"tap"`
	Oldnames          []string `json:"oldnames"`
	Aliases           []string `json:"aliases"`
	VersionedFormulae []string `json:"versioned_formulae"`
	Desc              string   `json:"desc"`
	License           string   `json:"license"`
	Homepage          string   `json:"homepage"`
	Versions          struct {
		Stable string `json:"stable"`
		Head   string `json:"head"`
		Bottle bool   `json:"bottle"`
	} `json:"versions"`
	URLs struct {
		Stable struct {
			URL      string `json:"url"`
			Tag      string `json:"tag"`
			Revision string `json:"revision"`
			Using    string `json:"using"`
			Checksum string `json:"checksum"`
		} `json:"stable"`
		Head struct {
			URL    string `json:"url"`
			Branch string `json:"branch"`
		} `json:"head"`
	} `json:"urls"`
	Revision      int `json:"revision"`
	VersionScheme int `json:"version_scheme"`
	Bottle        struct {
		Stable struct {
			Rebuild int `json:"rebuild"`
			Files   map[string]struct {
				Cellar string `json:"cellar"`
				URL    string `json:"url"`
				SHA256 string `json:"sha256"`
			} `json:"files"`
		} `json:"stable"`
	} `json:"bottle"`
	Requirements []struct {
		Name     string   `json:"name"`
		Version  *string  `json:"version"`
		Contexts []string `json:"contexts"`
	} `json:"requirements"`
	UsesFromMacosBounds     []map[string]string `json:"uses_from_macos_bounds"`
	PostInstallDefined      bool                `json:"post_install_defined"`
	KegOnly                 bool                `json:"keg_only"`
	KegOnlyReason           json.RawMessage     `json:"keg_only_reason"`
	BuildDependencies       []string            `json:"build_dependencies"`
	Dependencies            []string            `json:"dependencies"`
	TestDependencies        []string            `json:"test_dependencies"`
	RecommendedDependencies []string            `json:"recommended_dependencies"`
	OptionalDependencies    []string            `json:"optional_dependencies"`
	UsesFromMacos           []json.RawMessage   `json:"uses_from_macos"`
	ConflictsWith           []string            `json:"conflicts_with"`
	ConflictsWithReasons    []string            `json:"conflicts_with_reasons"`
	Caveats                 *string             `json:"caveats"`
	Deprecated              bool                `json:"deprecated"`
	DeprecationReason       *string             `json:"deprecation_reason"`
	Disabled                bool                `json:"disabled"`
	DisableReason           *string             `json:"disable_reason"`
	Service                 json.RawMessage     `json:"service"`
	TapGitHead              string              `json:"tap_git_head"`
	RubySourcePath          string              `json:"ruby_source_path"`
	Executables             []string            `json:"executables"`
}

// Cask mirrors the cask API fields mise-brew uses.
type Cask struct {
	Token     string            `json:"token"`
	FullToken string            `json:"full_token"`
	OldTokens []string          `json:"old_tokens"`
	Tap       string            `json:"tap"`
	Name      []string          `json:"name"`
	Desc      *string           `json:"desc"`
	Homepage  string            `json:"homepage"`
	URL       string            `json:"url"`
	Version   string            `json:"version"`
	SHA256    string            `json:"sha256"`
	Artifacts []json.RawMessage `json:"artifacts"`
	Caveats   *string           `json:"caveats"`
	DependsOn struct {
		Formula []string `json:"formula"`
		Cask    []string `json:"cask"`
	} `json:"depends_on"`
	AutoUpdates    *bool  `json:"auto_updates"`
	Deprecated     bool   `json:"deprecated"`
	Disabled       bool   `json:"disabled"`
	TapGitHead     string `json:"tap_git_head"`
	RubySourcePath string `json:"ruby_source_path"`
}

func (f *Formula) PkgVersion() string {
	if f.Revision > 0 {
		return f.Versions.Stable + "_" + strconv.Itoa(f.Revision)
	}
	return f.Versions.Stable
}

func (c *Cask) DescString() string {
	if c.Desc == nil {
		return ""
	}
	return *c.Desc
}

// ApiSource reads one of mise's cached API files (formula.json / cask.json),
// using mise's offset index when it matches the JSON file.
type ApiSource struct {
	Kind    string // "formula" or "cask"
	index   map[string][2]int64
	size    int64 // size of the JSON file the index describes
	indexOK bool
	loaded  bool
	all     []json.RawMessage
	byName  map[string]int
}

var FormulaAPI = &ApiSource{Kind: "formula"}

var CaskAPI = &ApiSource{Kind: "cask"}

func (s *ApiSource) JsonPath() string { return filepath.Join(paths.ApiDir(), s.Kind+".json") }

func (s *ApiSource) IndexPath() string { return filepath.Join(paths.ApiDir(), s.Kind+".index.json") }

func (s *ApiSource) Available() bool { return paths.FileExists(s.JsonPath()) }

func (s *ApiSource) ModTime() time.Time {
	fi, err := os.Stat(s.JsonPath())
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

func (s *ApiSource) loadIndex() {
	if s.index != nil || s.indexOK {
		return
	}
	s.indexOK = true // attempted
	data, err := os.ReadFile(s.IndexPath())
	if err != nil {
		return
	}
	var idx struct {
		SourceSize int64               `json:"source_size"`
		Casks      map[string][2]int64 `json:"casks"`
		Formulae   map[string][2]int64 `json:"formulae"`
	}
	if json.Unmarshal(data, &idx) != nil {
		return
	}
	fi, err := os.Stat(s.JsonPath())
	if err != nil || fi.Size() != idx.SourceSize {
		return
	}
	s.size = fi.Size()
	if idx.Casks != nil {
		s.index = idx.Casks
	} else {
		s.index = idx.Formulae
	}
}

// maxRecordSize bounds a single indexed record; real records are well under 1MB.
const maxRecordSize = 16 << 20

// Raw returns the raw JSON record for an exact name/token.
func (s *ApiSource) Raw(name string) (json.RawMessage, bool) {
	s.loadIndex()
	if s.index != nil {
		if loc, ok := s.index[name]; ok {
			if buf := s.readIndexed(loc); buf != nil {
				return buf, true
			}
			ui.Odebug("%s index entry for %s is invalid; reading %s", s.Kind, name, s.JsonPath())
		} else if !s.loaded {
			return nil, false
		}
	}
	s.loadAll()
	if i, ok := s.byName[name]; ok {
		return s.all[i], true
	}
	return nil, false
}

// readIndexed reads the record at an index location, or returns nil when the
// entry is out of bounds or does not hold a JSON object.
func (s *ApiSource) readIndexed(loc [2]int64) json.RawMessage {
	off, n := loc[0], loc[1]
	if off < 0 || n <= 0 || n > maxRecordSize || off > s.size-n {
		return nil
	}
	f, err := os.Open(s.JsonPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return nil
	}
	if buf[0] != '{' || !json.Valid(buf) {
		return nil
	}
	return buf
}

func (s *ApiSource) loadAll() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.byName = map[string]int{}
	data, err := os.ReadFile(s.JsonPath())
	if err != nil {
		return
	}
	if json.Unmarshal(data, &s.all) != nil {
		s.all = nil
		return
	}
	key := "name"
	if s.Kind == "cask" {
		key = "token"
	}
	for i, raw := range s.all {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		var n string
		_ = json.Unmarshal(m[key], &n)
		if n != "" {
			s.byName[n] = i
		}
	}
}

// All returns every raw record.
func (s *ApiSource) All() []json.RawMessage {
	s.loadAll()
	return s.all
}

// Names returns all names/tokens, sorted.
func (s *ApiSource) Names() []string {
	s.loadIndex()
	var names []string
	if s.index != nil {
		for n := range s.index {
			names = append(names, n)
		}
	} else {
		s.loadAll()
		for n := range s.byName {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func EnsureAPI() {
	if !FormulaAPI.Available() && !CaskAPI.Available() {
		ui.Odie("mise has not downloaded the Homebrew API yet. Run `brew update` first.")
	}
}

var FormulaCache = map[string]*Formula{}

// StripCoreTap removes homebrew/core/ or homebrew/cask/ prefixes.
func StripCoreTap(name string) string {
	for _, p := range []string{"homebrew/core/", "homebrew/homebrew-core/", "homebrew/cask/", "homebrew/homebrew-cask/"} {
		if strings.HasPrefix(name, p) {
			return name[len(p):]
		}
	}
	return name
}

// IsThirdPartyTapName reports whether name is owner/tap/formula outside homebrew/core|cask.
func IsThirdPartyTapName(name string) bool {
	return strings.Count(name, "/") == 2 && StripCoreTap(name) == name
}

// LookupFormula resolves names, aliases and old names via the API.
func LookupFormula(name string) *Formula {
	name = strings.ToLower(StripCoreTap(name))
	if IsThirdPartyTapName(name) {
		return LookupTapFormula(name)
	}
	if f, ok := FormulaCache[name]; ok {
		return f
	}
	var f *Formula
	if raw, ok := FormulaAPI.Raw(name); ok {
		f = &Formula{}
		if json.Unmarshal(raw, f) != nil {
			f = nil
		} else {
			applyVariations(raw, f)
		}
	}
	if f == nil {
		f = findFormulaByAlias(name)
	}
	if f == nil {
		if tap := InstalledTapOf(name); tap != "" {
			f = LookupTapFormula(tap + "/" + name)
		}
	}
	FormulaCache[name] = f
	return f
}

var AliasIndex map[string]string

func findFormulaByAlias(name string) *Formula {
	if AliasIndex == nil {
		AliasIndex = map[string]string{}
		for _, raw := range FormulaAPI.All() {
			var m struct {
				Name     string   `json:"name"`
				Aliases  []string `json:"aliases"`
				Oldnames []string `json:"oldnames"`
			}
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			for _, a := range m.Aliases {
				AliasIndex[a] = m.Name
			}
			for _, a := range m.Oldnames {
				if _, ok := AliasIndex[a]; !ok {
					AliasIndex[a] = m.Name
				}
			}
		}
	}
	if real, ok := AliasIndex[name]; ok && real != name {
		return LookupFormula(real)
	}
	return nil
}

var CaskCache = map[string]*Cask{}

func LookupCask(token string) *Cask {
	token = strings.ToLower(StripCoreTap(token))
	if IsThirdPartyTapName(token) {
		return LookupTapCask(token)
	}
	if c, ok := CaskCache[token]; ok {
		return c
	}
	var c *Cask
	if raw, ok := CaskAPI.Raw(token); ok {
		c = &Cask{}
		if json.Unmarshal(MergeVariationsRaw(raw), c) != nil {
			c = nil
		}
	}
	if c == nil {
		if real, ok := caskOldTokens()[token]; ok && real != token {
			c = LookupCask(real)
		}
	}
	CaskCache[token] = c
	return c
}

var CaskOldTokenIndex map[string]string

// caskOldTokens maps renamed cask tokens to their current token (built once).
func caskOldTokens() map[string]string {
	if CaskOldTokenIndex == nil {
		CaskOldTokenIndex = map[string]string{}
		for _, raw := range CaskAPI.All() {
			var m struct {
				Token     string   `json:"token"`
				OldTokens []string `json:"old_tokens"`
			}
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			for _, o := range m.OldTokens {
				if _, ok := CaskOldTokenIndex[o]; !ok {
					CaskOldTokenIndex[o] = m.Token
				}
			}
		}
	}
	return CaskOldTokenIndex
}

// MergeVariationsRaw overlays the current platform's variation onto a raw
// record (Homebrew's API.merge_variations) and drops the variations key, so
// every field read from it (url, sha256, artifacts, uninstall/zap stanzas,
// ...) matches this machine.
func MergeVariationsRaw(raw json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m["variations"] == nil {
		return raw
	}
	var vars map[string]map[string]json.RawMessage
	_ = json.Unmarshal(m["variations"], &vars)
	delete(m, "variations")
	for k, v := range vars[PlatformTag()] {
		m[k] = v
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

// rawMap returns the raw record as a generic map (for JSON output).
// keepVariations is set by `brew info --variations`.
var KeepVariations bool

func rawFormulaMap(name string) map[string]any {
	raw, ok := FormulaAPI.Raw(name)
	if !ok {
		return nil
	}
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if dec.Decode(&m) != nil {
		return nil
	}
	applyVariationsMap(m, KeepVariations)
	return m
}

func RawCaskMap(token string) map[string]any {
	raw, ok := CaskAPI.Raw(token)
	if !ok {
		return nil
	}
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if dec.Decode(&m) != nil {
		return nil
	}
	applyVariationsMap(m, KeepVariations)
	return m
}

// FormulaFromCore reports whether f comes from homebrew/core (or has no tap).
func FormulaFromCore(f *Formula) bool { return f.Tap == "" || f.Tap == "homebrew/core" }

// CaskFromCore reports whether c comes from homebrew/cask (or has no tap).
func CaskFromCore(c *Cask) bool { return c.Tap == "" || c.Tap == "homebrew/cask" }

// QualifiedFormulaName is the name a formula is addressed by: the bare name for core
// formulae, the tap-qualified full name for third-party taps.
func QualifiedFormulaName(f *Formula) string {
	if FormulaFromCore(f) || f.FullName == "" {
		return f.Name
	}
	return f.FullName
}

// QualifiedCaskToken is the cask counterpart of qualifiedFormulaName.
func QualifiedCaskToken(c *Cask) string {
	if CaskFromCore(c) || c.FullToken == "" {
		return c.Token
	}
	return c.FullToken
}

// FormulaMapFor returns the API-style JSON record of a formula: the cached API
// record for homebrew/core, otherwise one synthesized from the parsed formula
// (third-party taps have no API) with the same keys.
func FormulaMapFor(f *Formula) map[string]any {
	if FormulaFromCore(f) {
		if m := rawFormulaMap(f.Name); m != nil {
			return m
		}
	}
	b, err := json.Marshal(f)
	if err != nil {
		return nil
	}
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	if dec.Decode(&m) != nil {
		return nil
	}
	// Homebrew emits empty lists rather than null, and null for unset strings.
	for k, v := range m {
		if v == nil {
			switch k {
			case "oldnames", "aliases", "versioned_formulae", "requirements", "uses_from_macos", "uses_from_macos_bounds",
				"build_dependencies", "dependencies", "test_dependencies", "recommended_dependencies", "optional_dependencies",
				"conflicts_with", "conflicts_with_reasons", "executables":
				m[k] = []any{}
			}
		}
	}
	for _, k := range []string{"desc", "license", "homepage", "tap_git_head", "ruby_source_path"} {
		if m[k] == "" {
			m[k] = nil
		}
	}
	if vs, ok := m["versions"].(map[string]any); ok && vs["head"] == "" {
		vs["head"] = nil
	}
	if us, ok := m["urls"].(map[string]any); ok {
		for _, spec := range us {
			if sm, ok := spec.(map[string]any); ok {
				for k, v := range sm {
					if v == "" {
						sm[k] = nil
					}
				}
			}
		}
	}
	m["bottle"] = map[string]any{}
	m["options"] = []any{}
	m["link_overwrite"] = []any{}
	m["pinned"] = false
	m["outdated"] = false
	if KeepVariations {
		m["variations"] = map[string]any{}
	}
	return m
}

// UsesFromMacos returns (name, dep type) pairs of uses_from_macos in order ("" = runtime).
func UsesFromMacos(f *Formula) [][2]string {
	var out [][2]string
	for _, raw := range f.UsesFromMacos {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			out = append(out, [2]string{s, ""})
			continue
		}
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			for k, v := range m {
				typ := ""
				switch t := v.(type) {
				case string:
					typ = t
				case []any:
					if len(t) > 0 {
						typ, _ = t[0].(string)
					}
				}
				out = append(out, [2]string{k, typ})
			}
		}
	}
	return out
}

func ExpandCaveats(s string) string {
	return strings.NewReplacer("$HOMEBREW_PREFIX", paths.BrewPrefix(), "$HOMEBREW_CELLAR", paths.Cellar()).Replace(s)
}
