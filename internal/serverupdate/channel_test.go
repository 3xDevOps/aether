package serverupdate

import (
	"crypto/ed25519"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/selfupdate/releasetest"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

// A new server chooses its first channel from the pointers: stable only
// for a stable.json signed by a release key, beta while there is none, and
// no choice at all, following beta meanwhile, for one that does not verify.
// A channel already chosen is never replaced.
func TestNewServerChoosesItsChannelFromTheStablePointer(t *testing.T) {
	_, forger, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		key     ed25519.PrivateKey
		set     string
		want    string
		pending bool
	}{
		"verified":      {key: releasetest.Key, want: selfupdate.ChannelStable},
		"none":          {want: selfupdate.ChannelBeta},
		"forged":        {key: forger, want: selfupdate.ChannelBeta, pending: true},
		"nightly key":   {key: releasetest.NightlyKey, want: selfupdate.ChannelBeta, pending: true},
		"already chose": {key: releasetest.Key, set: selfupdate.ChannelNightly, want: selfupdate.ChannelNightly},
	} {
		t.Run(name, func(t *testing.T) {
			rel := channelHost(t)
			if tc.key != nil {
				publishPointer(t, rel, "stable", pointer("stable", "v0.9.0", 412), tc.key)
			}
			db, err := storetest.Open(filepath.Join(t.TempDir(), "aether.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if tc.set != "" {
				if err = db.SetReleaseChannel(t.Context(), tc.set); err != nil {
					t.Fatal(err)
				}
			}
			svc, err := New(Config{Store: db, Bus: newTestBus(t), StateDir: t.TempDir(), Now: time.Now, Checker: selfupdate.NewChecker(rel.URL, time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			if st := svc.runCheck(t.Context()); st.Channel != tc.want {
				t.Fatalf("check followed %q (%+v), want %q", st.Channel, st, tc.want)
			}
			stored, err := db.ReleaseChannel(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if wantStored := map[bool]string{false: tc.want, true: ""}[tc.pending]; stored != wantStored {
				t.Fatalf("stored channel = %q, want %q", stored, wantStored)
			}
			status, err := svc.Status(t.Context(), protocol.ServerUpdateStatusParams{})
			if err != nil {
				t.Fatal(err)
			}
			if status.Channel != tc.want || status.ChannelPending != tc.pending {
				t.Fatalf("status channel = %q pending %v, want %q pending %v", status.Channel, status.ChannelPending, tc.want, tc.pending)
			}
		})
	}
}
