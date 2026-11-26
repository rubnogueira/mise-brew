package paths

import (
	"path/filepath"
)

func MiseBrewCacheDir() string { return filepath.Join(miseCacheDir(), "mise-brew") }
