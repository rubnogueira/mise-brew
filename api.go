package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
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

// apiSource reads one of mise's cached API files (formula.json / cask.json),
// using mise's offset index when it matches the JSON file.
type apiSource struct {
	kind    string // "formula" or "cask"
	index   map[string][2]int64
	size    int64 // size of the JSON file the index describes
	indexOK bool
	loaded  bool
	all     []json.RawMessage
	byName  map[string]int
}

var formulaAPI = &apiSource{kind: "formula"}
var caskAPI = &apiSource{kind: "cask"}

func (s *apiSource) jsonPath() string  { return filepath.Join(apiDir(), s.kind+".json") }
func (s *apiSource) indexPath() string { return filepath.Join(apiDir(), s.kind+".index.json") }

func (s *apiSource) Available() bool { return fileExists(s.jsonPath()) }

func (s *apiSource) ModTime() time.Time {
	fi, err := os.Stat(s.jsonPath())
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

func (s *apiSource) loadIndex() {
	if s.index != nil || s.indexOK {
		return
	}
	s.indexOK = true // attempted
	data, err := os.ReadFile(s.indexPath())
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
	fi, err := os.Stat(s.jsonPath())
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
func (s *apiSource) Raw(name string) (json.RawMessage, bool) {
	s.loadIndex()
	if s.index != nil {
		if loc, ok := s.index[name]; ok {
			if buf := s.readIndexed(loc); buf != nil {
				return buf, true
			}
			odebug("%s index entry for %s is invalid; reading %s", s.kind, name, s.jsonPath())
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
func (s *apiSource) readIndexed(loc [2]int64) json.RawMessage {
	off, n := loc[0], loc[1]
	if off < 0 || n <= 0 || n > maxRecordSize || off > s.size-n {
		return nil
	}
	f, err := os.Open(s.jsonPath())
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

func (s *apiSource) loadAll() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.byName = map[string]int{}
	data, err := os.ReadFile(s.jsonPath())
	if err != nil {
		return
	}
	if json.Unmarshal(data, &s.all) != nil {
		s.all = nil
		return
	}
	key := "name"
	if s.kind == "cask" {
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
func (s *apiSource) All() []json.RawMessage {
	s.loadAll()
	return s.all
}

// Names returns all names/tokens, sorted.
func (s *apiSource) Names() []string {
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

func ensureAPI() {
	if !formulaAPI.Available() && !caskAPI.Available() {
		odie("mise has not downloaded the Homebrew API yet. Run `brew update` first.")
	}
}

var formulaCache = map[string]*Formula{}

// stripCoreTap removes homebrew/core/ or homebrew/cask/ prefixes.
func stripCoreTap(name string) string {
	for _, p := range []string{"homebrew/core/", "homebrew/homebrew-core/", "homebrew/cask/", "homebrew/homebrew-cask/"} {
		if strings.HasPrefix(name, p) {
			return name[len(p):]
		}
	}
	return name
}

// isThirdPartyTapName reports whether name is owner/tap/formula outside homebrew/core|cask.
func isThirdPartyTapName(name string) bool {
	return strings.Count(name, "/") == 2 && stripCoreTap(name) == name
}

// lookupFormula resolves names, aliases and old names via the API.
func lookupFormula(name string) *Formula {
	name = strings.ToLower(stripCoreTap(name))
	if isThirdPartyTapName(name) {
		return lookupTapFormula(name)
	}
	if f, ok := formulaCache[name]; ok {
		return f
	}
	var f *Formula
	if raw, ok := formulaAPI.Raw(name); ok {
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
		if tap := installedTapOf(name); tap != "" {
			f = lookupTapFormula(tap + "/" + name)
		}
	}
	formulaCache[name] = f
	return f
}

var aliasIndex map[string]string

func findFormulaByAlias(name string) *Formula {
	if aliasIndex == nil {
		aliasIndex = map[string]string{}
		for _, raw := range formulaAPI.All() {
			var m struct {
				Name     string   `json:"name"`
				Aliases  []string `json:"aliases"`
				Oldnames []string `json:"oldnames"`
			}
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			for _, a := range m.Aliases {
				aliasIndex[a] = m.Name
			}
			for _, a := range m.Oldnames {
				if _, ok := aliasIndex[a]; !ok {
					aliasIndex[a] = m.Name
				}
			}
		}
	}
	if real, ok := aliasIndex[name]; ok && real != name {
		return lookupFormula(real)
	}
	return nil
}

var caskCache = map[string]*Cask{}

func lookupCask(token string) *Cask {
	token = strings.ToLower(stripCoreTap(token))
	if isThirdPartyTapName(token) {
		return lookupTapCask(token)
	}
	if c, ok := caskCache[token]; ok {
		return c
	}
	var c *Cask
	if raw, ok := caskAPI.Raw(token); ok {
		c = &Cask{}
		if json.Unmarshal(mergeVariationsRaw(raw), c) != nil {
			c = nil
		}
	}
	if c == nil {
		if real, ok := caskOldTokens()[token]; ok && real != token {
			c = lookupCask(real)
		}
	}
	caskCache[token] = c
	return c
}

var caskOldTokenIndex map[string]string

// caskOldTokens maps renamed cask tokens to their current token (built once).
func caskOldTokens() map[string]string {
	if caskOldTokenIndex == nil {
		caskOldTokenIndex = map[string]string{}
		for _, raw := range caskAPI.All() {
			var m struct {
				Token     string   `json:"token"`
				OldTokens []string `json:"old_tokens"`
			}
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			for _, o := range m.OldTokens {
				if _, ok := caskOldTokenIndex[o]; !ok {
					caskOldTokenIndex[o] = m.Token
				}
			}
		}
	}
	return caskOldTokenIndex
}

// mergeVariationsRaw overlays the current platform's variation onto a raw
// record (Homebrew's API.merge_variations) and drops the variations key, so
// every field read from it (url, sha256, artifacts, uninstall/zap stanzas,
// ...) matches this machine.
func mergeVariationsRaw(raw json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m["variations"] == nil {
		return raw
	}
	var vars map[string]map[string]json.RawMessage
	_ = json.Unmarshal(m["variations"], &vars)
	delete(m, "variations")
	for k, v := range vars[platformTag()] {
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
var keepVariations bool

func rawFormulaMap(name string) map[string]any {
	raw, ok := formulaAPI.Raw(name)
	if !ok {
		return nil
	}
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if dec.Decode(&m) != nil {
		return nil
	}
	applyVariationsMap(m, keepVariations)
	return m
}

func rawCaskMap(token string) map[string]any {
	raw, ok := caskAPI.Raw(token)
	if !ok {
		return nil
	}
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if dec.Decode(&m) != nil {
		return nil
	}
	applyVariationsMap(m, keepVariations)
	return m
}

// formulaFromCore reports whether f comes from homebrew/core (or has no tap).
func formulaFromCore(f *Formula) bool { return f.Tap == "" || f.Tap == "homebrew/core" }

// caskFromCore reports whether c comes from homebrew/cask (or has no tap).
func caskFromCore(c *Cask) bool { return c.Tap == "" || c.Tap == "homebrew/cask" }

// qualifiedFormulaName is the name a formula is addressed by: the bare name for core
// formulae, the tap-qualified full name for third-party taps.
func qualifiedFormulaName(f *Formula) string {
	if formulaFromCore(f) || f.FullName == "" {
		return f.Name
	}
	return f.FullName
}

// qualifiedCaskToken is the cask counterpart of qualifiedFormulaName.
func qualifiedCaskToken(c *Cask) string {
	if caskFromCore(c) || c.FullToken == "" {
		return c.Token
	}
	return c.FullToken
}

// formulaMapFor returns the API-style JSON record of a formula: the cached API
// record for homebrew/core, otherwise one synthesized from the parsed formula
// (third-party taps have no API) with the same keys.
func formulaMapFor(f *Formula) map[string]any {
	if formulaFromCore(f) {
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
	if keepVariations {
		m["variations"] = map[string]any{}
	}
	return m
}

// usesFromMacos returns (name, dep type) pairs of uses_from_macos in order ("" = runtime).
func usesFromMacos(f *Formula) [][2]string {
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

func expandCaveats(s string) string {
	return strings.NewReplacer("$HOMEBREW_PREFIX", brewPrefix(), "$HOMEBREW_CELLAR", cellar()).Replace(s)
}
