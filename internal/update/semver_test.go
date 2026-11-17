package update

import "testing"

func TestParseVersion(t *testing.T) {
	good := map[string]string{
		"1.2.3": "1.2.3", "v1.2.3": "1.2.3", " 0.2.0 ": "0.2.0", "1.0.0-rc.1": "1.0.0-rc.1",
		"1.0.0+build5": "1.0.0", "0.0.0-dev.g1234567": "0.0.0-dev.g1234567",
	}
	for in, want := range good {
		v, err := ParseVersion(in)
		if err != nil || v.String() != want {
			t.Errorf("ParseVersion(%q) = %v, %v; want %s", in, v, err, want)
		}
	}
	for _, in := range []string{"", "dev", "1.2", "1.2.3.4", "01.2.3", "1.2.x", "1.2.3-", "1.2.3-a..b", "1.2.3-01", "1.2.3-é"} {
		if _, err := ParseVersion(in); err == nil {
			t.Errorf("ParseVersion(%q) succeeded", in)
		}
	}
}

func TestCompare(t *testing.T) {
	// Each row is in ascending order (the examples of semver.org section 11).
	order := []string{"0.9.9", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0", "10.0.0"}
	for i := range order {
		for j := range order {
			a, _ := ParseVersion(order[i])
			b, _ := ParseVersion(order[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := a.Compare(b); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
}

func TestDevBuildsAndNewer(t *testing.T) {
	for _, dev := range []string{"dev", "", "0.0.0-dev.g1234567", "garbage"} {
		if !IsDevBuild(dev) {
			t.Errorf("%q should be a development build", dev)
		}
		if Newer(dev, "9.9.9") {
			t.Errorf("a development build (%q) must never be offered an update", dev)
		}
	}
	for _, rel := range []string{"0.2.0", "1.0.0-rc.1"} {
		if IsDevBuild(rel) {
			t.Errorf("%q is not a development build", rel)
		}
	}
	cases := []struct {
		cur, latest string
		want        bool
	}{
		{"0.2.0", "0.3.0", true}, {"0.2.0", "0.2.0", false}, {"0.3.0", "0.2.0", false},
		{"0.2.0", "0.2.1", true}, {"1.0.0-rc.1", "1.0.0", true}, {"1.0.0", "1.0.0-rc.2", false},
		{"0.2.0", "not-a-version", false}, {"0.9.0", "0.10.0", true},
	}
	for _, c := range cases {
		if got := Newer(c.cur, c.latest); got != c.want {
			t.Errorf("Newer(%s, %s) = %v, want %v", c.cur, c.latest, got, c.want)
		}
	}
}
