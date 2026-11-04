package dictation

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/selfupdate"
)

// InstallCommand installs dictation; error messages name it.
const InstallCommand = "sudo aether-server dictation install"

const (
	manifestName = "install.json"
	engineBinary = "runtime/bin/sherpa-onnx-offline-websocket-server"
	releaseBase  = "https://github.com/k2-fsa/sherpa-onnx/releases/download/"
	// The engine's container images: glibc and libstdc++ for the CPU
	// engine, plus CUDA 12 and cuDNN 9 for the GPU one.
	cpuImage = "debian:bookworm-slim@sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3"
	gpuImage = "nvidia/cuda:12.9.1-cudnn-runtime-ubuntu24.04@sha256:d02c4310b6d57ca0b16cd80298bdb33a74187baafe2eccd8a6a16180ddc90802"
)

// artifact is one pinned sherpa-onnx tarball and the files dictation keeps
// from it, by their path below the tarball's top directory. Each lands
// below dest.
type artifact struct {
	name   string
	sha256 string
	size   int64
	dest   string
	files  []string
}

func (a artifact) url() string {
	if a.dest == "model" {
		return releaseBase + "asr-models/" + a.name + ".tar.bz2"
	}
	return releaseBase + "v2.0.1/" + a.name + ".tar.bz2"
}

var (
	runtimeAMD64 = artifact{
		name: "sherpa-onnx-v2.0.1-linux-x64-shared-no-tts", dest: "runtime",
		sha256: "b3b095f619059d54914027ff38f8a0a132ea3bbc5df32a212a9743a8bd1467b2", size: 25289904,
		files: []string{"bin/sherpa-onnx-offline-websocket-server", "lib/libonnxruntime.so"},
	}
	runtimeARM64 = artifact{
		name: "sherpa-onnx-v2.0.1-linux-aarch64-shared-cpu", dest: "runtime",
		sha256: "a8fc9fb3940d1aa3f83136767742b05068f00253f560d53c4f5aeb75ab4cb350", size: 27889232,
		files: []string{"bin/sherpa-onnx-offline-websocket-server", "lib/libonnxruntime.so"},
	}
	runtimeCUDA = artifact{
		name: "sherpa-onnx-v2.0.1-cuda-12.x-cudnn-9.x-onnxruntime1.28.2-linux-x64-gpu", dest: "runtime",
		sha256: "2aa541a65c1e7d921a053283419b70b30c1f6ae0a0f52f64b947ebe88c5f2a77", size: 393636488,
		files: []string{
			"bin/sherpa-onnx-offline-websocket-server", "lib/libonnxruntime.so",
			"lib/libonnxruntime_providers_shared.so", "lib/libonnxruntime_providers_cuda.so",
		},
	}
	modelInt8 = artifact{
		name: "sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8", dest: "model",
		sha256: "157c157bc51155e03e37d2466522a3a737dd9c72bb25f36eb18912964161e1ad", size: 482468385,
		files: []string{"encoder.int8.onnx", "decoder.int8.onnx", "joiner.int8.onnx", "tokens.txt"},
	}
	modelFP16 = artifact{
		name: "sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-fp16", dest: "model",
		sha256: "37f67a1a6c942dae27d345ee395fbd19e25ee48996faf70fca25779026054cf0", size: 1120982957,
		files: []string{"encoder.fp16.onnx", "decoder.fp16.onnx", "joiner.fp16.onnx", "tokens.txt"},
	}
)

// Manifest is what install.json records. It is written last, so a
// directory with one holds a complete install.
type Manifest struct {
	Image     string `json:"image"`
	Provider  string `json:"provider"`
	Runtime   string `json:"runtime"`
	Model     string `json:"model"`
	Precision string `json:"precision"`
	// Files is the directory below dir holding runtime/ and model/. Each
	// install writes a new one, so publishing install.json swaps installs
	// at once and a failed install leaves the previous one whole.
	Files string `json:"files"`
}

// Dir is dictation's directory below the server data directory.
func Dir(dataDir string) string { return filepath.Join(dataDir, "dictation") }

func pick(goarch string, gpu bool) (Manifest, []artifact, error) {
	switch {
	case gpu && goarch == "amd64":
		return Manifest{gpuImage, "cuda", runtimeCUDA.name, modelFP16.name, "fp16", ""}, []artifact{runtimeCUDA, modelFP16}, nil
	case gpu:
		return Manifest{}, nil, fmt.Errorf("--gpu needs linux/amd64; this server is linux/%s", goarch)
	case goarch == "amd64":
		return Manifest{cpuImage, "cpu", runtimeAMD64.name, modelInt8.name, "int8", ""}, []artifact{runtimeAMD64, modelInt8}, nil
	case goarch == "arm64":
		return Manifest{cpuImage, "cpu", runtimeARM64.name, modelInt8.name, "int8", ""}, []artifact{runtimeARM64, modelInt8}, nil
	}
	return Manifest{}, nil, fmt.Errorf("dictation runs on linux/amd64 and linux/arm64; this server is linux/%s", goarch)
}

// ReadManifest reads dir's install.json. A missing one is an error naming
// the file and the install command.
func ReadManifest(dir string) (Manifest, error) {
	path := filepath.Join(dir, manifestName)
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Manifest{}, fmt.Errorf("dictation is not installed: %s is missing; run: %s", path, InstallCommand)
	}
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, fmt.Errorf("read %s: %w; reinstall with: %s", path, err, InstallCommand)
	}
	return m, nil
}

// ImagePuller pulls the engine's container image. Satisfied by
// *runtime.Docker.
type ImagePuller interface {
	PullDictationImage(ctx context.Context, ref string) error
}

// Install downloads the pinned engine and model for goarch into dir,
// checks each tarball's sha256 before reading it, keeps only the files the
// engine runs, and pulls the engine's image. Any earlier install stays in
// place until the new one is complete.
func Install(ctx context.Context, dir, goarch string, gpu bool, images ImagePuller, w io.Writer) error {
	want, artifacts, err := pick(goarch, gpu)
	if err != nil {
		return err
	}
	have, haveErr := ReadManifest(dir)
	if haveErr == nil && have.Files != "" && withoutFiles(have) == want {
		_, _ = fmt.Fprintf(w, "Dictation is already installed in %s (%s, %s).\n", dir, want.Provider, want.Precision)
		return nil
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(dir, "files-")
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(staging)
		}
	}()
	if err = os.Chmod(staging, 0o755); err != nil {
		return err
	}
	want.Files = filepath.Base(staging)
	for _, a := range artifacts {
		if err = fetch(ctx, a, staging, w); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintf(w, "Pulling %s\n", want.Image)
	if err = images.PullDictationImage(ctx, want.Image); err != nil {
		return err
	}
	b, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		return err
	}
	pending := filepath.Join(dir, manifestName+".new")
	if err = os.WriteFile(pending, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err = os.Rename(pending, filepath.Join(dir, manifestName)); err != nil {
		return err
	}
	published = true
	if haveErr == nil && have.Files != "" {
		if err = os.RemoveAll(filepath.Join(dir, have.Files)); err != nil {
			return fmt.Errorf("remove the previous install: %w", err)
		}
	}
	_, _ = fmt.Fprintf(w, "Dictation installed in %s (%s, %s). The dashboard shows the microphone from its next page load; the server needs no restart.\n", dir, want.Provider, want.Precision)
	if !gpu && goarch == "amd64" && exec.CommandContext(ctx, "nvidia-smi", "-L").Run() == nil {
		_, _ = fmt.Fprintln(w, "nvidia-smi found a GPU: re-run with --gpu to transcribe on it (needs CUDA 12 and cuDNN 9).")
	}
	return nil
}

func fetch(ctx context.Context, a artifact, staging string, w io.Writer) error {
	f, err := os.CreateTemp(staging, "download-")
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()
	_, _ = fmt.Fprintf(w, "Downloading %s (%d MB)\n", a.url(), (a.size+1<<19)>>20)
	if err := selfupdate.DownloadPinned(ctx, a.url(), f, selfupdate.Asset{SHA256: a.sha256, Size: a.size}, w); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "Extracting %s\n", strings.Join(a.files, ", "))
	if err := extract(f, a, filepath.Join(staging, a.dest)); err != nil {
		return fmt.Errorf("extract %s: %w", a.name, err)
	}
	return nil
}

// extract writes the regular files of a that the tarball holds below its
// top directory into dest, and stops reading once it has them all.
func extract(r io.Reader, a artifact, dest string) error {
	missing := slices.Clone(a.files)
	tr := tar.NewReader(bzip2.NewReader(r))
	for len(missing) > 0 {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("the archive has no %s", strings.Join(missing, ", "))
		}
		if err != nil {
			return err
		}
		name, ok := strings.CutPrefix(hdr.Name, a.name+"/")
		i := slices.Index(missing, name)
		if !ok || i < 0 {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return fmt.Errorf("%s is not a regular file in the archive", hdr.Name)
		}
		missing = slices.Delete(missing, i, i+1)
		mode := os.FileMode(0o644)
		if strings.HasPrefix(name, "bin/") {
			mode = 0o755
		}
		if err := writeFile(filepath.Join(dest, name), tr, mode); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(path string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	return f.Close()
}

func withoutFiles(m Manifest) Manifest {
	m.Files = ""
	return m
}

// Remove deletes an install, manifest first so a partial removal never
// reads as installed.
func Remove(dir string) error {
	if err := os.Remove(filepath.Join(dir, manifestName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.RemoveAll(dir)
}
