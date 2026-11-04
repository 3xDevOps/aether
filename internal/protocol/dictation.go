package protocol

// MethodDictationTranscribe turns one recorded clip into text on the
// server. No connector may call it.
const MethodDictationTranscribe = "dictation.transcribe"

// FeatureDictation marks a server with dictation installed.
const FeatureDictation = "dictation"

// DictationTranscribeParams carries a base64 WAV: RIFF, PCM 16-bit
// little-endian, mono, 16000 Hz, at most 120 s. Hints are names the
// transcript should spell as given: workspaces, repositories, sessions.
type DictationTranscribeParams struct {
	AudioBase64 string   `json:"audio_base64"`
	Hints       []string `json:"hints,omitempty"`
}

type DictationTranscribeResult struct {
	Text string `json:"text"`
}
