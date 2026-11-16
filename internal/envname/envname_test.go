package envname

import "testing"

func TestGet(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"neither", map[string]string{}, ""},
		{"old only", map[string]string{"AETHER_X": "old"}, "old"},
		{"new only", map[string]string{"CLOUDFIELD_X": "new"}, "new"},
		{"both: new wins", map[string]string{"AETHER_X": "old", "CLOUDFIELD_X": "new"}, "new"},
		{"empty new falls back", map[string]string{"AETHER_X": "old", "CLOUDFIELD_X": ""}, "old"},
		{"empty old, new set", map[string]string{"AETHER_X": "", "CLOUDFIELD_X": "new"}, "new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(name string) string { return tc.env[name] }
			if got := Get(getenv, "AETHER_X"); got != tc.want {
				t.Fatalf("Get = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGetenv(t *testing.T) {
	t.Setenv("AETHER_ENVNAME_TEST", "old")
	t.Setenv("CLOUDFIELD_ENVNAME_TEST", "new")
	if got := Getenv("AETHER_ENVNAME_TEST"); got != "new" {
		t.Fatalf("Getenv = %q, want new", got)
	}
	t.Setenv("CLOUDFIELD_ENVNAME_TEST", "")
	if got := Getenv("AETHER_ENVNAME_TEST"); got != "old" {
		t.Fatalf("Getenv with an empty CLOUDFIELD_ name = %q, want old", got)
	}
}
