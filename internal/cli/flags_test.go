package cli

import (
	"testing"
)

func TestEveryCommandHasFlagSpec(t *testing.T) {
	for name := range commands {
		if _, ok := commandFlagLines[name]; !ok {
			t.Errorf("command %q has no flag spec", name)
		}
	}
}
