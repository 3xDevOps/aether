// Command nightly-plan decides what nightly.yml builds and what it prunes,
// from signed data only: the channel pointers and the nightly manifests,
// verified against the keys this build trusts. Tag names and the build:
// lines of annotated tags only say which manifests to read and which
// numbers a failed run already took.
//
//	nightly-plan manifests -refs <file> -releases <file> -pointers <dir>
//
// prints the published nightlies whose manifests state needs: those whose
// tag claims a build above every pointer.
//
//	nightly-plan state -refs <file> -releases <file> -pointers <dir> -manifests <dir>
//
// prints key=value lines: the newest nightly (tag, commit, publication),
// a nightly to resume (a tag a failed run left without a release), the tag
// and build of a new nightly, and whether nightly.json gets its first
// pointer.
//
//	nightly-plan prune -releases <file> -pointers <dir> -manifests <dir> -now <RFC 3339>
//
// prints "<tag> <commit>" for each published nightly to prune: older than
// 14 days, at or below stable.json's build, and not the newest nightly.
//
//	nightly-plan key-check
//
// signs a probe nightly manifest with the seed in AETHER_NIGHTLY_SIGNING_KEY
// and fails unless this build's nightly keys verify it and its release keys
// refuse a beta signed with it, so a run tags and builds nothing that its
// signer would then fail to sign.
//
// -refs is the output of
//
//	git for-each-ref --format='%(refname:strip=2) %(objecttype) %(objectname) %(*objectname)%0a%(contents)%00' 'refs/tags/v*'
//
// -releases holds "<tag> <draft> <published_at>" lines for every release,
// -pointers the channels release's nightly.json, beta.json, stable.json and
// their signatures, as far as they exist, and -manifests <tag>/aether-release.json
// with its .sig for each tag manifests or prune asked for.
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
)

// pruneAge is how long a nightly's release and images are kept.
const pruneAge = 14 * 24 * time.Hour

// keyEnv holds the nightly signing key, a base64 ed25519 seed.
const keyEnv = "AETHER_NIGHTLY_SIGNING_KEY"

var (
	fullSHA   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	nightRe   = regexp.MustCompile(`^v((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))-nightly\.([1-9][0-9]*)$`)
	stableRe  = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$`)
	betaRe    = regexp.MustCompile(`^v((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))-beta\.[1-9][0-9]*$`)
	buildLine = regexp.MustCompile(`(?m)^build: ([1-9][0-9]{0,7})$`)
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "nightly-plan:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: nightly-plan {manifests|state|prune|key-check} <flags>")
	}
	switch args[0] {
	case "manifests":
		return manifests(args[1:], out)
	case "state":
		return state(args[1:], out)
	case "prune":
		return prune(args[1:], out)
	case "key-check":
		return keyCheck(os.Getenv(keyEnv))
	}
	return fmt.Errorf("unknown subcommand %q; want manifests, state, prune or key-check", args[0])
}

// ref is one v* tag: the commit it names and, for an annotated tag, the
// build its message claims (0 when it has none).
type ref struct {
	tag, commit string
	build       int
}

// release is one GitHub release from the listing.
type release struct {
	draft     bool
	published time.Time
}

type input struct {
	refs     map[string]ref
	releases map[string]release
	pointers map[string]selfupdate.Pointer
	// nightlyFiles is whether nightly.json and nightly.json.sig exist.
	nightlyFiles [2]bool
	manifests    map[string]selfupdate.Manifest
}

func (in input) published(tag string) bool {
	r, ok := in.releases[tag]
	return ok && !r.draft
}

// signedMax is the highest build any pointer or verified nightly manifest
// names. Every build ever published is at or below it: a pruned or deleted
// nightly is below nightly.json, which never moves down.
func (in input) signedMax() int {
	high := 0
	for _, p := range in.pointers {
		high = max(high, p.Build)
	}
	for _, m := range in.manifests {
		high = max(high, m.Build)
	}
	return high
}

func manifests(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("nightly-plan manifests", flag.ContinueOnError)
	refsFile := fs.String("refs", "", "git for-each-ref output for refs/tags/v*")
	releasesFile := fs.String("releases", "", "file of <tag> <draft> <published_at> lines")
	pointers := fs.String("pointers", "", "directory holding the channels release's pointers")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *refsFile == "" || *releasesFile == "" || *pointers == "" || fs.NArg() != 0 {
		return errors.New("usage: nightly-plan manifests -refs <file> -releases <file> -pointers <dir>")
	}
	in, err := load(*refsFile, *releasesFile, *pointers, "")
	if err != nil {
		return err
	}
	high := in.signedMax()
	var tags []string
	for tag, r := range in.refs {
		if nightRe.MatchString(tag) && in.published(tag) && r.build > high {
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	for _, tag := range tags {
		if _, err := fmt.Fprintln(out, tag); err != nil {
			return err
		}
	}
	return nil
}

func state(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("nightly-plan state", flag.ContinueOnError)
	refsFile := fs.String("refs", "", "git for-each-ref output for refs/tags/v*")
	releasesFile := fs.String("releases", "", "file of <tag> <draft> <published_at> lines")
	pointers := fs.String("pointers", "", "directory holding the channels release's pointers")
	manifestDir := fs.String("manifests", "", "directory of <tag>/"+selfupdate.ManifestName)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *refsFile == "" || *releasesFile == "" || *pointers == "" || *manifestDir == "" || fs.NArg() != 0 {
		return errors.New("usage: nightly-plan state -refs <file> -releases <file> -pointers <dir> -manifests <dir>")
	}
	in, err := load(*refsFile, *releasesFile, *pointers, *manifestDir)
	if err != nil {
		return err
	}
	for _, ch := range []string{selfupdate.ChannelStable, selfupdate.ChannelBeta} {
		if _, ok := in.pointers[ch]; !ok {
			return fmt.Errorf("channels has no %s; nightlies start after the migration's first stable writes it", selfupdate.PointerName(ch))
		}
	}
	high := in.signedMax()
	for tag, r := range in.refs {
		if !nightRe.MatchString(tag) || !in.published(tag) || r.build <= high {
			continue
		}
		if _, ok := in.manifests[tag]; !ok {
			return fmt.Errorf("published %s claims build %d above every pointer, and its manifest was not read", tag, r.build)
		}
	}
	resume, err := pending(in, high)
	if err != nil {
		return err
	}
	first, err := firstPointer(in, resume)
	if err != nil {
		return err
	}
	newest := newestNightly(in)
	base := nightlyBase(in.pointers)
	n := 0
	for tag := range in.refs {
		if g := nightRe.FindStringSubmatch(tag); g != nil && g[1] == base {
			i, _ := strconv.Atoi(g[2])
			n = max(n, i)
		}
	}
	lines := []string{
		"newest-tag=" + newest.Version,
		"newest-commit=" + newest.Commit,
		"newest-published-at=" + newest.published,
		"resume-tag=" + resume.tag,
		"resume-commit=" + resume.commit,
		"resume-build=" + itoa(resume.build),
		fmt.Sprintf("next-tag=v%s-nightly.%d", base, n+1),
		fmt.Sprintf("next-build=%d", high+1),
		fmt.Sprintf("first-pointer=%t", first),
	}
	_, err = fmt.Fprintln(out, strings.Join(lines, "\n"))
	return err
}

func itoa(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// pending is the nightly a failed run tagged and did not publish, which
// the next run re-runs instead of taking new numbers. The signer tags only
// once every asset is built, at one above every signed build, and every
// later run resumes that tag first, so at most one tag is ever above the
// signed builds, and it is exactly one above them. Anything else is a tag
// no run of nightly.yml made.
func pending(in input, high int) (ref, error) {
	var above []ref
	for tag, r := range in.refs {
		if nightRe.MatchString(tag) && !in.published(tag) && r.build > high {
			above = append(above, r)
		}
	}
	sort.Slice(above, func(i, j int) bool { return above[i].tag < above[j].tag })
	switch {
	case len(above) == 0:
		return ref{}, nil
	case len(above) > 1:
		names := make([]string, len(above))
		for i, r := range above {
			names[i] = fmt.Sprintf("%s (build %d)", r.tag, r.build)
		}
		return ref{}, fmt.Errorf("tags %s have no published release and claim builds above every signed build %d; one failed run leaves one such tag, so an admin deletes the ones no run made (gh api -X DELETE repos/<owner>/<repo>/git/refs/tags/<tag>)", strings.Join(names, ", "), high)
	case above[0].build != high+1:
		return ref{}, fmt.Errorf("tag %s has no published release and claims build %d, but a failed run tags build %d, one above every signed build; an admin deletes it (gh api -X DELETE repos/<owner>/<repo>/git/refs/tags/%s)", above[0].tag, above[0].build, high+1, above[0].tag)
	}
	return above[0], nil
}

// firstPointer is true only while no nightly has ever been published: no
// nightly.json, and no nightly tag but the one being resumed. A missing
// nightly.json after that is a failed upload or a deletion, which a first
// pointer could move down.
func firstPointer(in input, resume ref) (bool, error) {
	_, verified := in.pointers[selfupdate.ChannelNightly]
	switch {
	case verified:
		return false, nil
	case in.nightlyFiles[0] || in.nightlyFiles[1]:
		return false, errors.New("channels has half of nightly.json and its .sig; restore the pair from the pointer-nightly artifact of the run that wrote it")
	}
	for tag := range in.refs {
		if nightRe.MatchString(tag) && tag != resume.tag {
			return false, fmt.Errorf("channels has no nightly.json, but nightly %s exists; restore nightly.json from the pointer-nightly artifact of the run that wrote it, or re-sign it with first-pointer for the newest nightly; if no run made %s, an admin deletes the tag", tag, tag)
		}
	}
	return true, nil
}

// newest is the newest published nightly by signed build.
type newest struct {
	selfupdate.Manifest
	published string
}

// newestNightly is the release nightly.json names, or a verified nightly
// manifest above it, which a run that published but failed to move the
// pointer leaves. Its publication time is its release's, or the pointer's
// once that release is gone.
func newestNightly(in input) newest {
	var n newest
	if p, ok := in.pointers[selfupdate.ChannelNightly]; ok {
		n.Version, n.Commit, n.Build = p.Tag, p.Commit, p.Build
		n.published = p.PublishedAt.UTC().Format(time.RFC3339)
	}
	for _, m := range in.manifests {
		if m.Build > n.Build {
			n.Manifest = m
		}
	}
	if r, ok := in.releases[n.Version]; ok && !r.draft {
		n.published = r.published.UTC().Format(time.RFC3339)
	}
	return n
}

// nightlyBase is the X.Y.Z nightlies are built at: the higher of the last
// stable with its minor bumped and the core of a beta above it, read from
// the signed stable.json and beta.json.
func nightlyBase(pointers map[string]selfupdate.Pointer) string {
	base := "v0.0.0"
	if g := stableRe.FindStringSubmatch(pointers[selfupdate.ChannelStable].Tag); g != nil {
		minor, _ := strconv.Atoi(g[2])
		base = fmt.Sprintf("v%s.%d.0", g[1], minor+1)
	}
	if g := betaRe.FindStringSubmatch(pointers[selfupdate.ChannelBeta].Tag); g != nil && selfupdate.Behind(base, "v"+g[1]) {
		base = "v" + g[1]
	}
	return strings.TrimPrefix(base, "v")
}

func prune(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("nightly-plan prune", flag.ContinueOnError)
	releasesFile := fs.String("releases", "", "file of <tag> <draft> <published_at> lines")
	pointers := fs.String("pointers", "", "directory holding the channels release's pointers")
	manifestDir := fs.String("manifests", "", "directory of <tag>/"+selfupdate.ManifestName)
	nowFlag := fs.String("now", "", "the time to measure ages from, RFC 3339")
	if err := fs.Parse(args); err != nil {
		return err
	}
	now, err := time.Parse(time.RFC3339, *nowFlag)
	if err != nil || *releasesFile == "" || *pointers == "" || *manifestDir == "" || fs.NArg() != 0 {
		return errors.New("usage: nightly-plan prune -releases <file> -pointers <dir> -manifests <dir> -now <RFC 3339>")
	}
	releases, err := readReleases(*releasesFile)
	if err != nil {
		return err
	}
	ptrs, _, err := readPointers(*pointers)
	if err != nil {
		return err
	}
	stable, ok := ptrs[selfupdate.ChannelStable]
	if !ok {
		return errors.New("stable.json is missing; nothing is pruned until it can be read and verified")
	}
	nightly, ok := ptrs[selfupdate.ChannelNightly]
	if !ok {
		return errors.New("nightly.json is missing; nothing is pruned until it can be read and verified")
	}
	var tags []string
	for tag, r := range releases {
		if nightRe.MatchString(tag) && !r.draft && now.Sub(r.published) >= pruneAge {
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	for _, tag := range tags {
		m, err := readManifest(filepath.Join(*manifestDir, tag, selfupdate.ManifestName), tag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "nightly-plan: keeping %s: %v\n", tag, err)
			continue
		}
		if m.Channel != selfupdate.ChannelNightly || m.Build > stable.Build || tag == nightly.Tag || m.Build >= nightly.Build {
			continue
		}
		if _, err := fmt.Fprintf(out, "%s %s\n", tag, m.Commit); err != nil {
			return err
		}
	}
	return nil
}

func load(refsFile, releasesFile, pointerDir, manifestDir string) (input, error) {
	var in input
	var err error
	if in.refs, err = readRefs(refsFile); err != nil {
		return in, err
	}
	if in.releases, err = readReleases(releasesFile); err != nil {
		return in, err
	}
	if in.pointers, in.nightlyFiles, err = readPointers(pointerDir); err != nil {
		return in, err
	}
	in.manifests = map[string]selfupdate.Manifest{}
	if manifestDir == "" {
		return in, nil
	}
	entries, err := os.ReadDir(manifestDir)
	if err != nil {
		return in, err
	}
	for _, e := range entries {
		tag := e.Name()
		r, ok := in.refs[tag]
		if !ok || !nightRe.MatchString(tag) {
			return in, fmt.Errorf("manifest for %s, which is not a nightly tag", tag)
		}
		m, err := readManifest(filepath.Join(manifestDir, tag, selfupdate.ManifestName), tag)
		if err != nil {
			return in, err
		}
		if m.Channel != selfupdate.ChannelNightly || m.Build != r.build || m.Commit != r.commit {
			return in, fmt.Errorf("%s signs channel %q, build %d, commit %s; its tag names build %d, commit %s", tag, m.Channel, m.Build, m.Commit, r.build, r.commit)
		}
		in.manifests[tag] = m
	}
	return in, nil
}

// readPointers verifies each pointer that exists. A pointer that exists
// and does not verify is an error, never a missing one.
func readPointers(dir string) (map[string]selfupdate.Pointer, [2]bool, error) {
	ptrs := map[string]selfupdate.Pointer{}
	var nightly [2]bool
	for _, ch := range []string{selfupdate.ChannelNightly, selfupdate.ChannelBeta, selfupdate.ChannelStable} {
		name := selfupdate.PointerName(ch)
		data, dataErr := os.ReadFile(filepath.Join(dir, name))
		sig, sigErr := os.ReadFile(filepath.Join(dir, name+".sig"))
		if ch == selfupdate.ChannelNightly {
			nightly = [2]bool{dataErr == nil, sigErr == nil}
		}
		switch {
		case errors.Is(dataErr, os.ErrNotExist) && errors.Is(sigErr, os.ErrNotExist):
			continue
		case dataErr != nil:
			return nil, nightly, fmt.Errorf("read %s: %w", name, dataErr)
		case sigErr != nil:
			return nil, nightly, fmt.Errorf("read %s.sig: %w", name, sigErr)
		}
		p, err := selfupdate.VerifyPointer(data, sig, ch)
		if err != nil {
			return nil, nightly, err
		}
		ptrs[ch] = p
	}
	return ptrs, nightly, nil
}

func readManifest(path, tag string) (selfupdate.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return selfupdate.Manifest{}, err
	}
	sig, err := os.ReadFile(path + ".sig")
	if err != nil {
		return selfupdate.Manifest{}, err
	}
	return selfupdate.VerifyManifest(data, sig, tag)
}

// readRefs parses git for-each-ref records: a header line "<tag> <type>
// <object> <peeled>", the tag message, and a NUL.
func readRefs(path string) (map[string]ref, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	refs := map[string]ref{}
	for _, rec := range bytes.Split(data, []byte{0}) {
		rec = bytes.TrimLeft(rec, "\n")
		if len(rec) == 0 {
			continue
		}
		header, message, _ := strings.Cut(string(rec), "\n")
		f := strings.Fields(header)
		var r ref
		switch {
		case len(f) == 4 && f[1] == "tag" && fullSHA.MatchString(f[3]):
			r = ref{tag: f[0], commit: f[3]}
			if g := buildLine.FindStringSubmatch(message); g != nil {
				r.build, _ = strconv.Atoi(g[1])
			}
		case len(f) == 3 && f[1] == "commit" && fullSHA.MatchString(f[2]):
			r = ref{tag: f[0], commit: f[2]}
		default:
			return nil, fmt.Errorf("%s: %q is not a tag record", path, header)
		}
		refs[r.tag] = r
	}
	return refs, nil
}

func readReleases(path string) (map[string]release, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	releases := map[string]release{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 || (f[1] != "true" && f[1] != "false") {
			return nil, fmt.Errorf("%s: %q is not a <tag> <draft> <published_at> line", path, line)
		}
		r := release{draft: f[1] == "true"}
		if !r.draft {
			if r.published, err = time.Parse(time.RFC3339, f[2]); err != nil {
				return nil, fmt.Errorf("%s: release %s: %w", path, f[0], err)
			}
		}
		releases[f[0]] = r
	}
	return releases, nil
}

// keyCheck never prints or writes the seed.
func keyCheck(seed string) error {
	raw, err := base64.StdEncoding.DecodeString(seed)
	if seed == "" || err != nil || len(raw) != ed25519.SeedSize {
		return fmt.Errorf("%s is not a base64 %d-byte ed25519 seed", keyEnv, ed25519.SeedSize)
	}
	key := ed25519.NewKeyFromSeed(raw)
	probe := func(tag, channel string) error {
		data, sig, err := selfupdate.SignManifest(selfupdate.Manifest{
			Schema: selfupdate.ManifestSchema, Channel: channel, Build: 1, Version: tag,
			Commit: strings.Repeat("0", 40), PublishedAt: time.Now().UTC(), StoreSchema: 1,
			Assets: map[string]selfupdate.Asset{"probe": {SHA256: strings.Repeat("0", 64), Size: 1}},
		}, key)
		if err != nil {
			return err
		}
		_, err = selfupdate.VerifyManifest(data, sig, tag)
		return err
	}
	if err := probe("v0.0.0-nightly.1", selfupdate.ChannelNightly); err != nil {
		return fmt.Errorf("main does not trust %s: %w; its public half must be in nightlyKeys (internal/selfupdate/keys.go) and shipped in a release before nightlies are turned on", keyEnv, err)
	}
	if probe("v0.0.0-beta.1", selfupdate.ChannelBeta) == nil {
		return fmt.Errorf("%s verifies a beta manifest, so it is a release key; the nightly environment must hold only the nightly key", keyEnv)
	}
	return nil
}
