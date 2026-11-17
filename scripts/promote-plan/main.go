// Command promote-plan decides what promote.yml builds. Its source
// subcommand finds the release a full commit SHA names:
//
//	promote-plan source -from <sha> -to <beta|stable> -tags <file> -published <file>
//
// and resolve verifies that release's signed manifest and the target
// channel's pointer against the keys this build trusts, then prints the
// new release as key=value lines for GITHUB_OUTPUT:
//
//	promote-plan resolve -from <tag> -to <beta|stable> [-version X.Y.Z] [-first-pointer] \
//	  -tags <file> -published <file> -manifest <file> -pointers <dir>
//
// -tags holds "<tag> <commit>" lines for every v* tag, -published the tags
// of published releases, -manifest the source's aether-release.json with
// its .sig beside it, and -pointers the channels release's stable.json,
// beta.json and their signatures, as far as they exist. The commit, build
// and channel come from the signed manifest, never from a tag name.
// scripts/promote-plan.sh gathers the files and runs it.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/3xDevOps/cloudfield/internal/selfupdate"
)

// migrationSource is the one pre-channel release Promote takes: the
// v0.8.7-beta.N that becomes the first stable, v0.8.7, signed as build 1
// (release channels plan, Migration).
var migrationSource = regexp.MustCompile(`^v0\.8\.7-beta\.[1-9][0-9]*$`)

const migrationVersion = "0.8.7"

var (
	fullSHA  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	core     = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	stableRe = regexp.MustCompile(`^v((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))$`)
	betaRe   = regexp.MustCompile(`^v((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))-beta\.([1-9][0-9]*)$`)
	nightRe  = regexp.MustCompile(`^v((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))-nightly\.([1-9][0-9]*)$`)
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "promote-plan:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: promote-plan {source|resolve} <flags>")
	}
	switch args[0] {
	case "source":
		return source(args[1:], out)
	case "resolve":
		return resolve(args[1:], out)
	}
	return fmt.Errorf("unknown subcommand %q; want source or resolve", args[0])
}

func source(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("promote-plan source", flag.ContinueOnError)
	from := fs.String("from", "", "full commit SHA of a nightly or beta")
	to := fs.String("to", "", "beta or stable")
	tagsFile := fs.String("tags", "", "file of <tag> <commit> lines")
	publishedFile := fs.String("published", "", "file of published release tags")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !fullSHA.MatchString(*from) || *tagsFile == "" || *publishedFile == "" || fs.NArg() != 0 {
		return errors.New("usage: promote-plan source -from <40 hex> -to <beta|stable> -tags <file> -published <file>")
	}
	form, err := sourceForm(*to)
	if err != nil {
		return err
	}
	tags, err := readTags(*tagsFile)
	if err != nil {
		return err
	}
	published, err := readLines(*publishedFile)
	if err != nil {
		return err
	}
	best := ""
	for tag, commit := range tags {
		if commit == *from && published[tag] && form.MatchString(tag) && (best == "" || selfupdate.Behind(best, tag)) {
			best = tag
		}
	}
	if best == "" {
		return fmt.Errorf("no published %s tag names commit %s", strings.TrimPrefix(form.String(), "^v"), *from)
	}
	_, err = fmt.Fprintln(out, best)
	return err
}

// sourceForm is the tag form whose release to: promotes from.
func sourceForm(to string) (*regexp.Regexp, error) {
	switch to {
	case selfupdate.ChannelBeta:
		return nightRe, nil
	case selfupdate.ChannelStable:
		return betaRe, nil
	}
	return nil, fmt.Errorf("to %q is not beta or stable", to)
}

type plan struct {
	tag, commit, channel string
	build, hotfix        int
	pointer, ifAbove     string
	first                bool
}

func resolve(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("promote-plan resolve", flag.ContinueOnError)
	from := fs.String("from", "", "tag of the release to promote")
	to := fs.String("to", "", "beta or stable")
	override := fs.String("version", "", "X.Y.Z for the new tag instead of the source's")
	first := fs.Bool("first-pointer", false, "the migration's first Promote, which writes the first stable.json and beta.json")
	tagsFile := fs.String("tags", "", "file of <tag> <commit> lines")
	publishedFile := fs.String("published", "", "file of published release tags")
	manifest := fs.String("manifest", "", "the source's "+selfupdate.ManifestName+", signature beside it")
	pointers := fs.String("pointers", "", "directory holding the channels release's pointers")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" || *tagsFile == "" || *publishedFile == "" || *manifest == "" || *pointers == "" || fs.NArg() != 0 {
		return errors.New("usage: promote-plan resolve -from <tag> -to <beta|stable> [-version X.Y.Z] [-first-pointer] -tags <file> -published <file> -manifest <file> -pointers <dir>")
	}
	if _, err := sourceForm(*to); err != nil {
		return err
	}
	if *override != "" && !core.MatchString(*override) {
		return fmt.Errorf("version %q is not X.Y.Z", *override)
	}
	tags, err := readTags(*tagsFile)
	if err != nil {
		return err
	}
	published, err := readLines(*publishedFile)
	if err != nil {
		return err
	}
	m, err := readManifest(*manifest, *from)
	if err != nil {
		return err
	}
	if !published[*from] {
		return fmt.Errorf("%s is not a published release; promote only a published one", *from)
	}
	if tagged, ok := tags[*from]; !ok || tagged != m.Commit {
		return fmt.Errorf("tag %s names %q, but its signed manifest names commit %s", *from, tagged, m.Commit)
	}
	p, version, err := promotion(m, *to, *override, *first, tags, published)
	if err != nil {
		return err
	}
	if p.tag, err = newTag(*to, version, m.Commit, tags, published); err != nil {
		return err
	}
	if err = checkPointers(*pointers, p); err != nil {
		return err
	}
	return p.write(out)
}

// promotion checks that m may be promoted to to and returns the release it
// becomes, without its tag, and the version core of that tag.
func promotion(m selfupdate.Manifest, to, override string, first bool, tags map[string]string, published map[string]bool) (plan, string, error) {
	p := plan{commit: m.Commit, channel: to, build: m.Build, hotfix: m.Hotfix, pointer: to, first: first}
	if to == selfupdate.ChannelStable {
		p.ifAbove = selfupdate.ChannelBeta
	}
	if m.Channel == "" {
		if to != selfupdate.ChannelStable || !first || !migrationSource.MatchString(m.Version) || m.Build != 0 {
			return plan{}, "", fmt.Errorf("%s is a release from before channels; the only one Promote takes is a v0.8.7-beta.N to stable with first-pointer, the migration's first stable", m.Version)
		}
		if override != "" && override != migrationVersion {
			return plan{}, "", fmt.Errorf("the migration's first stable is v%s, not v%s", migrationVersion, override)
		}
		p.build, p.hotfix = 1, 0
		return p, migrationVersion, nil
	}
	if first {
		return plan{}, "", fmt.Errorf("first-pointer is only for promoting the pre-channel v0.8.7-beta.N to stable; %s is a %s release", m.Version, m.Channel)
	}
	want := selfupdate.ChannelNightly
	if to == selfupdate.ChannelStable {
		want = selfupdate.ChannelBeta
	}
	if m.Channel != want {
		return plan{}, "", fmt.Errorf("%s is a %s release; to %s promotes only a published %s", m.Version, m.Channel, to, want)
	}
	version := strings.TrimPrefix(m.Version, "v")
	version, _, _ = strings.Cut(version, "-")
	if override != "" {
		version = override
	}
	if base := nightlyBase(tags, published); selfupdate.Behind("v"+version, "v"+base) {
		return plan{}, "", fmt.Errorf("version %s is below the current nightly base %s; pass version %s or above", version, base, base)
	}
	return p, version, nil
}

// nightlyBase is the X.Y.Z nightlies are built at: the higher of the last
// stable with its minor bumped and the core of the highest beta above it,
// among published releases. A tag a failed Promote left without one does
// not count, so a new dispatch of that promotion can resume it.
func nightlyBase(tags map[string]string, published map[string]bool) string {
	stable := ""
	for tag := range tags {
		if published[tag] && stableRe.MatchString(tag) && (stable == "" || selfupdate.Behind(stable, tag)) {
			stable = tag
		}
	}
	base := "v0.0.0"
	if stable != "" {
		parts := strings.Split(strings.TrimPrefix(stable, "v"), ".")
		minor, _ := strconv.Atoi(parts[1])
		base = fmt.Sprintf("v%s.%d.0", parts[0], minor+1)
	}
	for tag := range tags {
		if g := betaRe.FindStringSubmatch(tag); g != nil && published[tag] && selfupdate.Behind(base, "v"+g[1]) {
			base = "v" + g[1]
		}
	}
	return strings.TrimPrefix(base, "v")
}

// newTag is vX.Y.Z for stable, or the next vX.Y.Z-beta.N. A tag that
// exists without a published release is what a failed Promote leaves: the
// same commit resumes it, any other is refused.
func newTag(to, version, commit string, tags map[string]string, published map[string]bool) (string, error) {
	last, next := "v"+version, "v"+version
	if _, ok := tags[last]; !ok {
		last = ""
	}
	if to == selfupdate.ChannelBeta {
		n := 0
		for tag := range tags {
			if g := betaRe.FindStringSubmatch(tag); g != nil && g[1] == version {
				if i, _ := strconv.Atoi(g[2]); i > n {
					n = i
				}
			}
		}
		last, next = "", fmt.Sprintf("v%s-beta.%d", version, n+1)
		if n > 0 {
			last = fmt.Sprintf("v%s-beta.%d", version, n)
		}
	}
	switch {
	case last == "" || published[last] && to == selfupdate.ChannelBeta:
		return next, nil
	case !published[last] && tags[last] == commit:
		return last, nil
	case !published[last]:
		return "", fmt.Errorf("tag %s names %s and has no published release, so a Promote of that commit failed after tagging it; re-run that Promote, or have an admin delete the tag (gh api -X DELETE repos/<owner>/<repo>/git/refs/tags/%s) before promoting %s", last, tags[last], last, commit)
	}
	return "", fmt.Errorf("%s is already released; pass a higher version", last)
}

// checkPointers refuses a release not above the target channel's pointer and,
// for the migration's first stable, any pointer that already exists.
func checkPointers(dir string, p plan) error {
	present := func(name string) bool {
		_, err := os.Lstat(filepath.Join(dir, name))
		return err == nil
	}
	if p.first {
		for _, ch := range []string{selfupdate.ChannelStable, selfupdate.ChannelBeta} {
			name := selfupdate.PointerName(ch)
			if present(name) || present(name+".sig") {
				return fmt.Errorf("channels already has %s; first-pointer is only for the first stable, before any pointer exists", name)
			}
		}
		return nil
	}
	name := selfupdate.PointerName(p.channel)
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return fmt.Errorf("read the current %s: %w; a channel without a pointer is the migration's first stable only (first-pointer)", name, err)
	}
	sig, err := os.ReadFile(filepath.Join(dir, name+".sig"))
	if err != nil {
		return fmt.Errorf("read the current %s.sig: %w", name, err)
	}
	cur, err := selfupdate.VerifyPointer(data, sig, p.channel)
	if err != nil {
		return fmt.Errorf("the current %s does not verify: %w", name, err)
	}
	// Strictly above: at an equal build the pointer would move to a
	// relabel that the edge image tag, ordered the same way, refuses.
	if p.build < cur.Build || p.build == cur.Build && p.hotfix <= cur.Hotfix {
		return fmt.Errorf("%s names %s at %s; %s is not above it, and a channel only moves up",
			name, cur.Tag, cur.Mark(), selfupdate.Mark{Build: p.build, Hotfix: p.hotfix})
	}
	return nil
}

func (p plan) write(out io.Writer) error {
	_, err := fmt.Fprintf(out, "tag=%s\ncommit=%s\nchannel=%s\nbuild=%d\nhotfix=%d\npointer=%s\npointer-if-above=%s\nfirst-pointer=%t\n",
		p.tag, p.commit, p.channel, p.build, p.hotfix, p.pointer, p.ifAbove, p.first)
	return err
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

func readTags(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	tags := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		tag, commit, ok := strings.Cut(sc.Text(), " ")
		if !ok || !fullSHA.MatchString(commit) {
			return nil, fmt.Errorf("%s: %q is not a <tag> <commit> line", path, sc.Text())
		}
		tags[tag] = commit
	}
	return tags, sc.Err()
}

func readLines(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := map[string]bool{}
	for _, l := range strings.Fields(string(data)) {
		lines[l] = true
	}
	return lines, nil
}
