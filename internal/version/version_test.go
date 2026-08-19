package version

import (
	"strings"
	"testing"
)

func TestStringFormat(t *testing.T) {
	s := String()
	if !strings.HasPrefix(s, "shardstore v") {
		t.Errorf("String() = %q, want prefix %q", s, "shardstore v")
	}
	for _, want := range []string{"commit ", "built "} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q, missing %q", s, want)
		}
	}
}