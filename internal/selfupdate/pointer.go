package selfupdate

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// PointerSchema is the channel pointer format this build reads.
const PointerSchema = 1

// channelsTag is the fixed prerelease whose assets are the channel
// pointers. It matches no release tag, so it never starts a release.
const channelsTag = "channels"

// Pointer is a channel's verified <channel>.json: the newest release, by
// (build, hotfix), among those the channel accepts.
type Pointer struct {
	Schema      int       `json:"schema"`
	Channel     string    `json:"channel"`
	Tag         string    `json:"tag"`
	Commit      string    `json:"commit"`
	Build       int       `json:"build"`
	Hotfix      int       `json:"hotfix"`
	StoreSchema int       `json:"store_schema"`
	PublishedAt time.Time `json:"published_at"`

	// nightlySigned reports that only a nightly key signed the pointer.
	nightlySigned bool
}

// PointerName is the asset that holds channel's pointer.
func PointerName(channel string) string { return channel + ".json" }

// PointerURL is where the repository at baseURL publishes channel's
// pointer; its signature is the same URL with .sig appended.
func PointerURL(baseURL, channel string) string {
	return baseURL + "/releases/download/" + channelsTag + "/" + PointerName(channel)
}

var pointerKeys = jsonKeys(reflect.TypeFor[Pointer]())

var commitHex = regexp.MustCompile(`^[0-9a-f]{40}$`)

func knownChannel(channel string) error {
	switch channel {
	case ChannelNightly, ChannelBeta, ChannelStable:
		return nil
	}
	return fmt.Errorf("%q is not a release channel (nightly, beta or stable)", channel)
}

// FetchPointer downloads channel's pointer and signature from the
// repository at baseURL and verifies them.
func FetchPointer(ctx context.Context, baseURL, channel string) (Pointer, error) {
	if err := knownChannel(channel); err != nil {
		return Pointer{}, err
	}
	url := PointerURL(baseURL, channel)
	data, err := fetchSmall(ctx, url)
	if err != nil {
		return Pointer{}, fmt.Errorf("channel pointer %s: %w", PointerName(channel), err)
	}
	sig, err := fetchSmall(ctx, url+".sig")
	if err != nil {
		return Pointer{}, fmt.Errorf("channel pointer %s: %w", PointerName(channel), err)
	}
	return VerifyPointer(data, sig, channel)
}

// VerifyPointer checks sig over the exact bytes of data under the same key
// rule as a manifest: a release key signs any pointer, a nightly key only
// one whose channel is nightly. It then parses data and checks it is
// channel's pointer.
func VerifyPointer(data, sig []byte, channel string) (Pointer, error) {
	name := PointerName(channel)
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return Pointer{}, fmt.Errorf("channel pointer %s.sig is not a base64 ed25519 signature", name)
	}
	release, nightly := signedBy(releaseKeys, data, raw), signedBy(nightlyKeys, data, raw)
	if !release && !nightly {
		return Pointer{}, fmt.Errorf("channel pointer %s is not signed by a trusted release key; do not follow it", name)
	}
	if err := canonicalKeys(data, pointerKeys); err != nil {
		return Pointer{}, fmt.Errorf("parse channel pointer %s: %w; do not follow it", name, err)
	}
	var p Pointer
	if err := json.Unmarshal(data, &p); err != nil {
		return Pointer{}, fmt.Errorf("parse channel pointer %s: %w", name, err)
	}
	if p.Schema != PointerSchema {
		return Pointer{}, fmt.Errorf("channel pointer %s has schema %d; this aether reads schema %d", name, p.Schema, PointerSchema)
	}
	if p.Channel != channel {
		return Pointer{}, fmt.Errorf("channel pointer %s is signed for channel %q; do not follow it", name, p.Channel)
	}
	if !release && p.Channel != ChannelNightly {
		return Pointer{}, fmt.Errorf("channel pointer %s is signed by a nightly key but its channel is %q, not nightly; do not follow it", name, p.Channel)
	}
	if err := p.valid(); err != nil {
		return Pointer{}, fmt.Errorf("channel pointer %s: %w; do not follow it", name, err)
	}
	p.nightlySigned = !release
	return p, nil
}

// valid checks the fields a pointer must carry. Build 0 names a release
// from before channels, which only beta.json does, once, while existing
// installs move to the first channel-aware release.
func (p Pointer) valid() error {
	switch {
	case !ValidTag(p.Tag):
		return fmt.Errorf("tag %q is not a release tag", p.Tag)
	case !commitHex.MatchString(p.Commit):
		return fmt.Errorf("commit %q is not 40 lowercase hex digits", p.Commit)
	case p.Build < 0 || p.Hotfix < 0:
		return fmt.Errorf("build %d and hotfix %d; neither may be negative", p.Build, p.Hotfix)
	case p.Build == 0 && (p.Hotfix != 0 || p.Channel != ChannelBeta):
		return fmt.Errorf("build 0 with hotfix %d on %s; only beta names a release from before channels, without a hotfix", p.Hotfix, p.Channel)
	case p.StoreSchema < 1:
		return fmt.Errorf("store schema %d is not positive", p.StoreSchema)
	case p.PublishedAt.IsZero():
		return errors.New("no published_at")
	}
	return nil
}

// Mark is the highest (build, hotfix) an install has accepted from one
// channel's pointer.
type Mark struct {
	Build  int `json:"build"`
	Hotfix int `json:"hotfix"`
}

// Marks holds a Mark per channel. A channel with a mark has had a pointer
// verified, so its check never falls back to /releases/latest again.
type Marks map[string]Mark

// Mark is where p places its channel.
func (p Pointer) Mark() Mark { return Mark{Build: p.Build, Hotfix: p.Hotfix} }

// replayed refuses a pointer for channel that names got, below the mark
// seen.
func replayed(channel string, got, seen Mark) error {
	return fmt.Errorf("channel pointer %s names %s, below %s seen before", PointerName(channel), got, seen)
}

func (m Mark) below(o Mark) bool {
	return m.Build < o.Build || m.Build == o.Build && m.Hotfix < o.Hotfix
}

// String reads "build 412", or "build 412 hotfix 1" for a hotfix.
func (m Mark) String() string {
	if m.Hotfix == 0 {
		return fmt.Sprintf("build %d", m.Build)
	}
	return fmt.Sprintf("build %d hotfix %d", m.Build, m.Hotfix)
}

// FollowChannel finds the newest release on channel at baseURL through its
// signed pointer, and returns that release's manifest with marks raised to
// the pointer. marks is never modified.
//
// A pointer below the channel's mark is refused: an old pointer replayed
// can only fail checks. Until a pointer has been verified for the channel,
// a missing pointer falls back to /releases/latest, offering its release
// only when the channel accepts it; once one has, a missing pointer is a
// failed check, so replacing the asset never sends the install back to
// Latest.
func FollowChannel(ctx context.Context, baseURL, channel string, marks Marks) (Manifest, Marks, error) {
	m, _, raised, err := FollowChannelPointer(ctx, baseURL, channel, marks)
	return m, raised, err
}

// FollowChannelPointer is FollowChannel that also returns the pointer it
// accepted, zero when it fell back to /releases/latest or refused one.
func FollowChannelPointer(ctx context.Context, baseURL, channel string, marks Marks) (Manifest, Pointer, Marks, error) {
	seen, verified := marks[channel]
	p, err := FetchPointer(ctx, baseURL, channel)
	var status *statusError
	if !verified && errors.As(err, &status) && status.code == http.StatusNotFound {
		m, latestErr := LatestManifestFrom(ctx, baseURL)
		if latestErr == nil {
			if refused := m.AcceptedOn(channel); refused != nil {
				return Manifest{}, Pointer{}, marks, fmt.Errorf("%w: %w", ErrNoRelease, refused)
			}
		}
		return m, Pointer{}, marks, latestErr
	}
	if err != nil {
		return Manifest{}, Pointer{}, marks, err
	}
	name := PointerName(channel)
	if verified && p.Mark().below(seen) {
		return Manifest{}, Pointer{}, marks, replayed(channel, p.Mark(), seen)
	}
	m, err := fetchManifest(ctx, baseURL, p.Tag)
	if err != nil {
		return Manifest{}, Pointer{}, marks, fmt.Errorf("channel pointer %s names %s: %w", name, p.Tag, err)
	}
	switch {
	case m.Commit != p.Commit:
		return Manifest{}, Pointer{}, marks, fmt.Errorf("channel pointer %s names %s at commit %s, but its manifest says commit %s; do not install it", name, p.Tag, p.Commit, m.Commit)
	case m.Build != p.Build || m.Hotfix != p.Hotfix:
		return Manifest{}, Pointer{}, marks, fmt.Errorf("channel pointer %s names %s at %s, but its manifest says %s; do not install it", name, p.Tag, p.Mark(), Mark{Build: m.Build, Hotfix: m.Hotfix})
	case m.StoreSchema != p.StoreSchema:
		return Manifest{}, Pointer{}, marks, fmt.Errorf("channel pointer %s names %s at store schema %d, but its manifest says store schema %d; do not install it", name, p.Tag, p.StoreSchema, m.StoreSchema)
	case p.nightlySigned && m.Channel != ChannelNightly:
		return Manifest{}, Pointer{}, marks, fmt.Errorf("channel pointer %s is signed by a nightly key but names %s, a %s release; do not install it", name, p.Tag, m.Channel)
	}
	if err := m.AcceptedOn(channel); err != nil {
		return Manifest{}, Pointer{}, marks, fmt.Errorf("channel pointer %s: %w; do not install it", name, err)
	}
	raised := maps.Clone(marks)
	if raised == nil {
		raised = Marks{}
	}
	raised[channel] = p.Mark()
	return m, p, raised, nil
}
