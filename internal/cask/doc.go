// Package cask uninstalls casks natively: it runs the uninstall and zap stanzas of the
// cask API (launchctl, quit, signal, login items, pkgutil, delete, trash, ...) and
// removes the artifacts mise recorded, never touching protected system paths.
package cask
