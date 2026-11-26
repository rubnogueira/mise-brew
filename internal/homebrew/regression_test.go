package homebrew

import (
	"testing"
)

// GoReleaser writes `Hardware::CPU.intel? and Hardware::CPU.is_64_bit?`.
func TestRbCondRubyAnd(t *testing.T) {
	t.Setenv("MISEBREW_BOTTLE_TAG", "arm64_linux")
	if v, ok := rbCond("Hardware::CPU.intel? and Hardware::CPU.is_64_bit?"); !ok || v {
		t.Errorf("intel and 64-bit on arm64 = %v, %v; want false, true", v, ok)
	}
	if v, ok := rbCond("Hardware::CPU.arm? and Hardware::CPU.is_64_bit?"); !ok || !v {
		t.Errorf("arm and 64-bit on arm64 = %v, %v; want true, true", v, ok)
	}
	// `and` inside a word is not the keyword.
	if _, ok := rbCond("OS.android?"); ok {
		t.Error("unknown condition evaluated")
	}
}
