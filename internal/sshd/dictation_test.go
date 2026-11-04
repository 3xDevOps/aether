package sshd

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/dictation"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// blockingEngine answers every clip with text, after release lets it.
type blockingEngine struct {
	started chan struct{}
	release chan struct{}
}

func (f *blockingEngine) Transcribe(ctx context.Context, _ []byte) (string, error) {
	f.started <- struct{}{}
	select {
	case <-f.release:
		return "  open the sync d log  ", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (f *blockingEngine) Close() error { return nil }

const audioMarker = "dictated-audio-marker!!!"

func testWAV(rate uint32, channels uint16, dataBytes int) string {
	b := make([]byte, 44+dataBytes)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+dataBytes))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], channels)
	binary.LittleEndian.PutUint32(b[24:], rate)
	binary.LittleEndian.PutUint32(b[28:], rate*uint32(channels)*2)
	binary.LittleEndian.PutUint16(b[32:], channels*2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(dataBytes))
	for i := 44; i < len(b); i += len(audioMarker) {
		copy(b[i:], audioMarker)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// Dictation runs through dispatch like any member call: unknown keys,
// pending members and connectors are refused, bad audio names its problem, the feature and
// the not-installed error follow the install manifest, and a member has
// one request at a time. No audio is written anywhere: not to the store,
// its audit entries or any file the server keeps.
func TestDictationTranscribe(t *testing.T) {
	dir := t.TempDir()
	engine := &blockingEngine{started: make(chan struct{}, 8), release: make(chan struct{})}
	svc := dictation.New(dir, engine)
	e := newTestEnv(t, func(c *Config) { c.Services.Dictation = svc })
	ctx := context.Background()
	call := func(member domain.MemberID, wav string, hints []string) (string, *protocol.Error) {
		params, _ := json.Marshal(protocol.DictationTranscribeParams{AudioBase64: wav, Hints: hints})
		raw, perr := e.srv.dispatch(ctx, member, protocol.MethodDictationTranscribe, params)
		if perr != nil {
			return "", perr
		}
		var out protocol.DictationTranscribeResult
		if jerr := json.Unmarshal(raw, &out); jerr != nil {
			t.Fatal(jerr)
		}
		return out.Text, nil
	}
	features := func() []string {
		t.Helper()
		var info protocol.ServerInfoResult
		if cerr := controlAs(t, e, e.signer).Call(protocol.MethodServerInfo, nil, &info); cerr != nil {
			t.Fatal(cerr)
		}
		return info.Features
	}
	clip := testWAV(16000, 1, 32000)

	if slices.Contains(features(), protocol.FeatureDictation) {
		t.Fatal("server.info lists dictation before it is installed")
	}
	_, perr := call(e.member.ID, clip, nil)
	if perr == nil || perr.Code != protocol.CodeUnavailable ||
		!strings.Contains(perr.Message, filepath.Join(dir, "install.json")) || !strings.Contains(perr.Message, dictation.InstallCommand) {
		t.Fatalf("not installed = %v; want unavailable naming the manifest and the install command", perr)
	}
	if err := os.WriteFile(filepath.Join(dir, "install.json"), []byte(`{"provider":"cpu","precision":"int8"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(features(), protocol.FeatureDictation) {
		t.Fatal("server.info omits dictation once installed")
	}

	if c, derr := e.dialWith(newSigner(t), nil); derr == nil {
		_ = c.Close()
		t.Fatal("a key no member holds connected")
	}
	_, pending := addMember(t, e, "Pat", domain.RoleCollaborator, true)
	if _, perr := call(pending.ID, clip, nil); perr == nil || perr.Code != protocol.CodeDenied {
		t.Fatalf("pending member = %v, want denied", perr)
	}
	connector := e.srv.ConnectorLocal(ConnectorGrant{Member: e.member.ID}, "con-test", "ChatGPT",
		[]string{ConnectorScopeRead, ConnectorScopeWrite, ConnectorScopeControl})
	params, _ := json.Marshal(protocol.DictationTranscribeParams{AudioBase64: clip})
	if _, perr := connector.Call(ctx, protocol.MethodDictationTranscribe, params); perr == nil || perr.Code != protocol.CodeDenied {
		t.Fatalf("connector = %v, want denied", perr)
	}

	for _, tc := range []struct {
		name, wav string
		hints     []string
		want      string
	}{
		{"not base64", "%%%", nil, "audio_base64 is not standard base64"},
		{"not a WAV", base64.StdEncoding.EncodeToString([]byte("OggS0000WAVE")), nil, "not a RIFF WAVE file"},
		{"48 kHz", testWAV(48000, 1, 96000), nil, "audio is 48000 Hz; dictation needs 16000 Hz mono PCM16 WAV"},
		{"stereo", testWAV(16000, 2, 64000), nil, "audio has 2 channels"},
		{"too long", testWAV(16000, 1, dictation.MaxDataBytes+2), nil, "audio is 120.0 s; dictation takes at most 120 s"},
		{"too many hints", clip, make([]string, 201), "hints has 201 entries; at most 200"},
		{"empty hint", clip, []string{""}, "hints[0] is 0 bytes"},
	} {
		if _, perr := call(e.member.ID, tc.wav, tc.hints); perr == nil || perr.Code != protocol.CodeInvalidParams || !strings.Contains(perr.Message, tc.want) {
			t.Errorf("%s = %v; want invalid params containing %q", tc.name, perr, tc.want)
		}
	}

	results := make(chan *protocol.Error, 1)
	var text string
	go func() {
		var perr *protocol.Error
		text, perr = call(e.member.ID, clip, []string{"syncd"})
		results <- perr
	}()
	<-engine.started
	if _, perr := call(e.member.ID, clip, nil); perr == nil || perr.Code != protocol.CodeUnavailable || !strings.Contains(perr.Message, "you already have a dictation") {
		t.Fatalf("second request from one member = %v, want unavailable", perr)
	}
	close(engine.release)
	if perr := <-results; perr != nil || text != "open the syncd log" {
		t.Fatalf("transcript = %q, %v; want the hinted, trimmed text", text, perr)
	}
	root := filepath.Dir(t.TempDir())
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), audioMarker) || strings.Contains(string(b), clip[1000:1100]) {
			t.Errorf("%s holds the dictated audio", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
