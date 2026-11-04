# Dictation

Dictation lets you speak a task or message instead of typing it. The dashboard
records a clip in the browser and sends it to your server, which turns it into
text with a speech-to-text engine running on the server itself and returns the
text to the box you were typing in. No audio or text goes to a third party.

Dictation is off until an administrator installs it on the server. Once it is
installed, `server.info` lists the `dictation` feature and the dashboard shows
a microphone button from its next page load. The server needs no restart.

The engine is NVIDIA's Parakeet TDT 0.6B v2 model, English only, run by
[sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx)'s
`sherpa-onnx-offline-websocket-server` in a Docker container the server
starts on the first clip.

## Using dictation in the dashboard

With dictation installed, a microphone button sits next to **Send** in a
session's message box and in the assistant's, and in the corner of the **New
session** task box. Tap it, speak, and tap it again; or hold it while you speak
and let go. The browser asks for the microphone on the first press. While it
records, the button turns red and shows a level meter and the elapsed time.
Escape discards the recording, and recording stops by itself at 2 minutes.

The text goes where your cursor was when you started, replacing any selected
text, and is never sent on its own: read it, edit it, then press **Send**. A
failure shows on the button as the browser or server reported it, such as
`Microphone: NotAllowedError: Permission denied` when microphone access is
blocked, or the server's `run: sudo aether-server dictation install` when
dictation is not installed. Browsers offer the microphone only on `https` pages
and on `localhost`, so a dashboard opened over plain `http` from another
machine has no microphone.

## Server requirements

| | CPU (default) | GPU (`--gpu`) |
| --- | --- | --- |
| Platform | linux/amd64 or linux/arm64 | linux/amd64 with an NVIDIA GPU and the [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html) |
| Disk | 690 MB in the data directory, 1.2 GB free while installing, and a Debian image of about 100 MB | 1.9 GB in the data directory, 3.1 GB free while installing, and the `nvidia/cuda` CUDA 12 and cuDNN 9 runtime image, several GB |
| Memory | about 0.8 GB while the engine runs, up to about 1.75 GB on a 120 s clip | not measured |
| CPU | one clip at a time on `nproc / 4` cores (1 to 4), at nice 10; about 0.1 CPU-second per second of audio | the engine runs with `--provider=cuda` |

The engine starts only while the host's `MemAvailable` in `/proc/meminfo` is
at least 2 GiB, and its container is capped at 2.5 GiB of memory with no swap.

The project has not yet run `--gpu` on GPU hardware.

## Install

```sh
sudo aether-server dictation install          # CPU
sudo aether-server dictation install --gpu    # NVIDIA GPU
sudo aether-server dictation status
sudo aether-server dictation remove
```

All three read `--data-dir` and `--config` like `serve`. `install` downloads
the two pinned tarballs below over HTTPS, checks each one's size and SHA-256
before reading it, and keeps only the files the engine runs, in a new
`files-<n>/` directory (`runtime/bin/`, `runtime/lib/` and `model/`) below
`<data-dir>/dictation/` (default `/var/lib/aether/dictation`). It then pulls
the engine's image, pinned by digest, so the first clip does not wait for a
pull. Writing `install.json`, which names the `files-<n>/` directory, publishes
the install; only then is the previous one deleted, so an install that fails
or is interrupted leaves the earlier one working. A running engine is replaced
on the next clip after a new install. Running `install` again with the same
choice does nothing. On amd64 without `--gpu`, it suggests `--gpu` when
`nvidia-smi -L` succeeds.

The server never installs or updates dictation on its own. `remove` deletes
`install.json` first, then everything in the directory but `install.lock`.
`install` and `remove` hold that lock while they run, so a second one started
meanwhile fails at once and names it. The image stays in Docker until you
remove it.

| File | SHA-256 | Used for |
| --- | --- | --- |
| [sherpa-onnx-v2.0.1-linux-x64-shared-no-tts.tar.bz2](https://github.com/k2-fsa/sherpa-onnx/releases/download/v2.0.1/sherpa-onnx-v2.0.1-linux-x64-shared-no-tts.tar.bz2) | `b3b095f619059d54914027ff38f8a0a132ea3bbc5df32a212a9743a8bd1467b2` | Engine, amd64 CPU |
| [sherpa-onnx-v2.0.1-linux-aarch64-shared-cpu.tar.bz2](https://github.com/k2-fsa/sherpa-onnx/releases/download/v2.0.1/sherpa-onnx-v2.0.1-linux-aarch64-shared-cpu.tar.bz2) | `a8fc9fb3940d1aa3f83136767742b05068f00253f560d53c4f5aeb75ab4cb350` | Engine, arm64 CPU |
| [sherpa-onnx-v2.0.1-cuda-12.x-cudnn-9.x-onnxruntime1.28.2-linux-x64-gpu.tar.bz2](https://github.com/k2-fsa/sherpa-onnx/releases/download/v2.0.1/sherpa-onnx-v2.0.1-cuda-12.x-cudnn-9.x-onnxruntime1.28.2-linux-x64-gpu.tar.bz2) | `2aa541a65c1e7d921a053283419b70b30c1f6ae0a0f52f64b947ebe88c5f2a77` | Engine, amd64 GPU |
| [sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8.tar.bz2](https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8.tar.bz2) | `157c157bc51155e03e37d2466522a3a737dd9c72bb25f36eb18912964161e1ad` | Model, CPU |
| [sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-fp16.tar.bz2](https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-fp16.tar.bz2) | `37f67a1a6c942dae27d345ee395fbd19e25ee48996faf70fca25779026054cf0` | Model, GPU |
| `debian:bookworm-slim@sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3` | the digest in the reference | Image, CPU |
| `nvidia/cuda:12.9.1-cudnn-runtime-ubuntu24.04@sha256:d02c4310b6d57ca0b16cd80298bdb33a74187baafe2eccd8a6a16180ddc90802` | the digest in the reference | Image, GPU |

## The engine container

The first clip starts one container, `aether-dictation-<owner>`, labelled
`aether.managed=true` and `aether.dictation=<owner>`, where `<owner>` is a
short hash of the dictation directory, so two servers on one Docker daemon
never touch each other's engine. It is removed after 10 minutes without a
clip, when the server stops, and when the same server, restarted, finds one
left over; a clip after the engine died starts a new one. Loading
the model takes a few seconds, so the first clip after a pause is slower. A
request that ends while the model loads, or while its clip is transcribed,
leaves the engine running for the next clip.

The engine listens on every interface and has no authentication, so its
container has no network (`--network none`): its port exists only on the
container's own loopback. The container's entrypoint is the `aether-server`
binary, mounted read-only, which starts the engine and relays the Unix socket
`<data-dir>/dictation/sock/engine.sock` (mode `0600`, in a `0700` directory)
to it; the server talks only to that socket. The container also has a
read-only root, every capability dropped, `no-new-privileges`, at most 256
processes, the engine and model mounted read-only, and runs as uid 65534
(`nobody`), or as the server's own uid when the server is not root. `--gpu`
adds every GPU to it.

## How a clip is transcribed

The dashboard calls `dictation.transcribe` (`POST /api/v1/dictation.transcribe`
through either [dashboard gateway](local-gateway.md)) with
`{"audio_base64": "<WAV>", "hints": [...]}` and gets `{"text": "..."}`, empty
when the clip has no speech. Any approved member may call it; pending members,
[connectors](connectors.md) and the [assistant](assistant.md) cannot.

Limits:

- Audio is a RIFF WAV of 16-bit little-endian PCM, mono, 16000 Hz, at most
  120 s (3,840,000 bytes of samples). The dashboard resamples before upload.
  The HTTP body cap for this method is 6 MiB.
- The server transcribes one clip at a time. Four more may wait, each for at
  most 1 minute; each member has at most one transcribing or waiting. A clip has 3 minutes once its turn
  comes, and `aether gui` waits 4 minutes for the answer.
- `hints` holds at most 200 names, each 1 to 80 bytes: the dashboard sends
  workspace, repository and session names. After transcription, a run of one to
  four words is respelled as a hint only when its letters and digits,
  lowercased and joined, equal the hint's exactly: "sync d" becomes `syncd`,
  "login mounts" becomes `loginMounts`, "aether core" becomes `aether-core`.
  Nothing approximate is replaced.

## Errors

| Code | Message starts with | Meaning |
| --- | --- | --- |
| `-32602` | `audio is 48000 Hz; dictation needs 16000 Hz mono PCM16 WAV`, `audio is 130.0 s; ...`, `audio is not a RIFF WAVE file`, `hints has 201 entries` | The request is wrong; the message names the problem. |
| `-32004` | `dictation is not installed: /var/lib/aether/dictation/install.json is missing; run: sudo aether-server dictation install` | Install it. |
| `-32004` | `not enough memory for dictation: the engine needs 2.0 GiB available and MemAvailable is 1.3 GiB` | Free memory, for example by closing sessions, and try again. |
| `-32004` | `start the dictation engine: runtime: ...` | Docker refused the container; the message carries Docker's error. For `--gpu` without the NVIDIA Container Toolkit, Docker reports that it cannot select a device driver with the `gpu` capability. |
| `-32004` | `the dictation engine exited with status N: ...` | The engine failed; the message ends with the last 3 lines of its output. A missing CUDA library shows here. |
| `-32004` | `the dictation engine was killed: out of memory (limit 2.5 GiB)` | The container hit its memory cap. |
| `-32004` | `dictation is busy: 4 requests queued; try again`, `dictation is busy: waited 1m0s and N requests are still queued; try again` or `dictation is busy: you already have a dictation transcribing or queued` | Wait for the queue. |

## Privacy

The clip exists only in the server's memory while its request runs. The server
never writes it to disk, logs it, records it in audit entries (which keep only
ids) or puts it in an error message. The engine runs with
`--log-file=/dev/null` and its container with Docker's `none` log driver: the
server keeps only the engine's last 4 KiB of output in memory, for the error
it reports if the engine fails. The server does not
keep the transcript either; it goes back to your browser, and from there
wherever you put it, such as a task. See [privacy.md](privacy.md#dictation).

## Licences

`aether-server` does not bundle the engine or the model; `install` downloads
them from the sherpa-onnx releases above.

- The model is [NVIDIA Parakeet TDT 0.6B v2](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v2)
  by NVIDIA, under [CC-BY-4.0](https://creativecommons.org/licenses/by/4.0/),
  in the ONNX export sherpa-onnx publishes.
- sherpa-onnx is under the [Apache-2.0](https://github.com/k2-fsa/sherpa-onnx/blob/master/LICENSE)
  licence. Its runtime tarballs include ONNX Runtime, under the MIT licence.
