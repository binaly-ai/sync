package pathx

import (
	"encoding/json"
	"os"
	"testing"
)

// The same vectors the server's tests use. If either side's rules drift, this fails.
func TestKeyVectors(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/pathkeys.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ In, Key string }
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if got := Key(c.In); got != c.Key {
			t.Errorf("Key(%q) = %q, want %q", c.In, got, c.Key)
		}
	}
}

func TestIsUnder(t *testing.T) {
	cases := []struct {
		prefix, path string
		want         bool
	}{
		{`X:\Lab\Lit`, `x:/lab/lit/a.pdf`, true},
		{`X:\Lab\Lit`, `X:\Lab\Lit`, true},
		{`X:\Lab\Lit`, `X:\Lab\Literature\a.pdf`, false},
		{`\\nas\lab`, `\\NAS\Lab\x\y.dat`, true},
		{``, `/anything`, true},
	}
	for _, c := range cases {
		if got := IsUnder(c.prefix, c.path); got != c.want {
			t.Errorf("IsUnder(%q, %q) = %v", c.prefix, c.path, got)
		}
	}
}
