package buildinfo

// Version is the mise-brew version; release builds override it (see the package doc).
var Version = "0.1.0"

// DefaultHomebrewVersion is reported by `brew --version` when no receipt says otherwise.
const DefaultHomebrewVersion = "5.1.15"
