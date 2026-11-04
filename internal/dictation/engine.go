package dictation

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	containerruntime "github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/sysstats"
)

// Transcriber turns PCM16 mono 16 kHz samples into text.
type Transcriber interface {
	Transcribe(ctx context.Context, pcm []byte) (string, error)
	Close() error
}

// Containers runs the engine's container. Satisfied by
// *runtime.Docker.
type Containers interface {
	StartDictation(ctx context.Context, spec containerruntime.DictationSpec) (containerruntime.ID, error)
	DictationState(ctx context.Context, id containerruntime.ID) (containerruntime.DictationState, error)
	RemoveDictation(ctx context.Context) error
}

const (
	idleStop     = 10 * time.Minute
	startTimeout = 2 * time.Minute
	minMemory    = 2 << 30
	memoryLimit  = 5 << 29
	// enginePort is the engine's port on the container's own loopback.
	enginePort = 6006
	outputTail = 4 << 10
	// nobody: the engine never runs as root, even for a root server.
	engineUID = 65534

	engineRoot    = "/opt/dictation"
	socketDir     = "/run/dictation"
	wrapperTarget = "/aether-dictation"
)

// sidecar runs the installed engine in a container on the first request
// and removes it after idleStop without one.
type sidecar struct {
	dir string
	// wrapper is this binary, which the container runs as RunEngine.
	wrapper    string
	containers Containers
	memory     func() (uint64, error)

	mu       sync.Mutex
	id       containerruntime.ID
	timer    *time.Timer
	inflight int
	closed   bool
}

// NewEngine runs the install in dir in containers.
func NewEngine(dir string, containers Containers) (Transcriber, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &sidecar{dir: dir, wrapper: self, containers: containers, memory: sysstats.MemoryAvailable}, nil
}

func (s *sidecar) socket() string { return filepath.Join(s.dir, "sock", "engine.sock") }

func (s *sidecar) Transcribe(ctx context.Context, pcm []byte) (string, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", unavailable("dictation is shutting down")
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	id, err := s.running(ctx)
	if err != nil {
		s.mu.Unlock()
		return "", err
	}
	s.inflight++
	s.mu.Unlock()
	text, err := talk(ctx, s.socket(), pcm)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inflight--
	if err != nil {
		err = s.failure(id, "dictation engine: "+err.Error())
		s.stop(id)
		return "", err
	}
	if s.id == id {
		s.timer = time.AfterFunc(idleStop, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.inflight == 0 {
				s.stop(id)
			}
		})
	}
	return text, nil
}

// running returns the live engine container, starting one if there is
// none. Callers hold s.mu.
func (s *sidecar) running(ctx context.Context) (containerruntime.ID, error) {
	if s.id != "" {
		if st, err := s.containers.DictationState(ctx, s.id); err == nil && st.Running {
			return s.id, nil
		}
		s.stop(s.id)
	}
	m, err := ReadManifest(s.dir)
	if err != nil {
		return "", unavailable(err.Error())
	}
	avail, err := s.memory()
	if err != nil {
		return "", unavailable("dictation cannot check free memory: " + err.Error())
	}
	if avail < minMemory {
		return "", unavailable(fmt.Sprintf("not enough memory for dictation: the engine needs %.1f GiB available and MemAvailable is %.1f GiB", gib(minMemory), gib(avail)))
	}
	user, err := s.prepareSocket()
	if err != nil {
		return "", unavailable("prepare the dictation engine's socket directory: " + err.Error())
	}
	entrypoint := []string{wrapperTarget, "dictation", "engine", socketDir + "/engine.sock", strconv.Itoa(enginePort), "--"}
	id, err := s.containers.StartDictation(ctx, containerruntime.DictationSpec{
		Image:      m.Image,
		Entrypoint: append(entrypoint, engineArgs(m)...),
		User:       user,
		ReadOnly: map[string]string{
			s.wrapper:                       wrapperTarget,
			filepath.Join(s.dir, "runtime"): engineRoot + "/runtime",
			filepath.Join(s.dir, "model"):   engineRoot + "/model",
		},
		Writable:    map[string]string{filepath.Join(s.dir, "sock"): socketDir},
		CPUs:        float64(threads()),
		MemoryBytes: memoryLimit,
		GPU:         m.Provider == "cuda",
	})
	if err != nil {
		return "", unavailable("start the dictation engine: " + err.Error())
	}
	s.id = id
	if err := s.awaitReady(ctx, id); err != nil {
		s.stop(id)
		return "", err
	}
	return id, nil
}

// prepareSocket makes the directory the engine creates its socket in,
// writable only by the user the engine runs as, and returns that user.
func (s *sidecar) prepareSocket() (string, error) {
	dir := filepath.Dir(s.socket())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Remove(s.socket()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	uid, gid := os.Geteuid(), os.Getegid()
	if uid == 0 {
		uid, gid = engineUID, engineUID
		if err := os.Chown(dir, uid, gid); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%d:%d", uid, gid), nil
}

// awaitReady waits for the engine's socket, which appears once the model
// has loaded.
func (s *sidecar) awaitReady(ctx context.Context, id containerruntime.ID) error {
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	for {
		if c, err := net.Dial("unix", s.socket()); err == nil {
			_ = c.Close()
			return nil
		}
		st, err := s.containers.DictationState(ctx, id)
		if err != nil {
			return unavailable("dictation engine: " + err.Error())
		}
		if !st.Running {
			return exited(st)
		}
		select {
		case <-ctx.Done():
			return unavailable(fmt.Sprintf("the dictation engine did not start within %s: %v", startTimeout, ctx.Err()))
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// failure explains why a request to engine id failed: how the engine
// exited, if it did, or else msg.
func (s *sidecar) failure(id containerruntime.ID, msg string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if st, err := s.containers.DictationState(ctx, id); err == nil && !st.Running {
		return exited(st)
	}
	return unavailable(msg)
}

func exited(st containerruntime.DictationState) error {
	if st.OOMKilled {
		return unavailable(fmt.Sprintf("the dictation engine was killed: out of memory (limit %.1f GiB)", gib(memoryLimit)))
	}
	return unavailable(fmt.Sprintf("the dictation engine exited with status %d\n%s", st.ExitCode, tail([]byte(st.Output))))
}

// stop removes engine id if it is the live one. Callers hold s.mu. A
// container that fails to go is removed again before the next start.
func (s *sidecar) stop(id containerruntime.ID) {
	if s.id != id {
		return
	}
	s.id = ""
	if s.timer != nil {
		s.timer.Stop()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = s.containers.RemoveDictation(ctx)
}

func (s *sidecar) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.id == "" {
		return nil
	}
	s.id = ""
	if s.timer != nil {
		s.timer.Stop()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.containers.RemoveDictation(ctx)
}

func threads() int { return min(max(runtime.NumCPU()/4, 1), 4) }

func engineArgs(m Manifest) []string {
	model := func(part string) string { return engineRoot + "/model/" + part + "." + m.Precision + ".onnx" }
	return []string{
		engineRoot + "/" + engineBinary,
		"--port=" + strconv.Itoa(enginePort),
		"--model-type=nemo_transducer",
		"--provider=" + m.Provider,
		"--num-threads=" + strconv.Itoa(threads()),
		"--num-work-threads=1",
		"--num-io-threads=1",
		"--max-batch-size=1",
		"--max-utterance-length=" + strconv.Itoa(MaxSeconds+5),
		"--log-file=/dev/null",
		"--tokens=" + engineRoot + "/model/tokens.txt",
		"--encoder=" + model("encoder"),
		"--decoder=" + model("decoder"),
		"--joiner=" + model("joiner"),
	}
}

// talk sends pcm to the engine as sherpa-onnx's offline websocket server
// takes it - one binary message of the sample rate, the byte count and
// float32 samples - reads the JSON result and ends with "Done".
func talk(ctx context.Context, socket string, pcm []byte) (string, error) {
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
	conn, _, err := websocket.Dial(ctx, "ws://engine/", &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.CloseNow() }()
	n := len(pcm) / 2
	msg := make([]byte, 8+4*n)
	binary.LittleEndian.PutUint32(msg[0:4], SampleRate)
	binary.LittleEndian.PutUint32(msg[4:8], uint32(4*n))
	for i := range n {
		v := float32(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768
		binary.LittleEndian.PutUint32(msg[8+4*i:], math.Float32bits(v))
	}
	if err = conn.Write(ctx, websocket.MessageBinary, msg); err != nil {
		return "", err
	}
	_, reply, err := conn.Read(ctx)
	if err != nil {
		return "", err
	}
	var result struct {
		Text string `json:"text"`
	}
	if err = json.Unmarshal(reply, &result); err != nil {
		return "", fmt.Errorf("engine reply is not JSON: %w", err)
	}
	// The engine closes the connection once it reads "Done".
	if err = conn.Write(ctx, websocket.MessageText, []byte("Done")); err == nil {
		_, _, _ = conn.Read(ctx)
	}
	return result.Text, nil
}

func gib(b uint64) float64 { return float64(b) / (1 << 30) }

func tail(b []byte) string {
	b = bytes.TrimSpace(b)
	if len(b) > outputTail {
		b = b[len(b)-outputTail:]
	}
	return string(b)
}
