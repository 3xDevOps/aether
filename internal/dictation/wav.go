package dictation

import (
	"encoding/binary"
	"fmt"
)

const (
	SampleRate   = 16000
	MaxSeconds   = 120
	MaxDataBytes = SampleRate * 2 * MaxSeconds
)

const wantFormat = "dictation needs 16000 Hz mono PCM16 WAV"

// ParseWAV returns the PCM16 samples of a RIFF WAV that is exactly the
// format the engine takes, as a slice of b.
func ParseWAV(b []byte) ([]byte, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, fmt.Errorf("audio is not a RIFF WAVE file; %s", wantFormat)
	}
	sawFormat := false
	for rest := b[12:]; len(rest) >= 8; {
		id, size := string(rest[0:4]), binary.LittleEndian.Uint32(rest[4:8])
		rest = rest[8:]
		if uint64(size) > uint64(len(rest)) {
			return nil, fmt.Errorf("audio %q chunk declares %d bytes but %d remain", id, size, len(rest))
		}
		body := rest[:size]
		switch id {
		case "fmt ":
			if err := checkFormat(body); err != nil {
				return nil, err
			}
			sawFormat = true
		case "data":
			if !sawFormat {
				return nil, fmt.Errorf("audio has no fmt chunk before its data; %s", wantFormat)
			}
			if size%2 != 0 {
				return nil, fmt.Errorf("audio data is %d bytes, not whole 16-bit samples", size)
			}
			if size == 0 {
				return nil, fmt.Errorf("audio has no samples")
			}
			if size > MaxDataBytes {
				return nil, fmt.Errorf("audio is %.1f s; dictation takes at most %d s", float64(size)/(SampleRate*2), MaxSeconds)
			}
			return body, nil
		}
		rest = rest[size:]
		if size%2 == 1 && len(rest) > 0 {
			rest = rest[1:]
		}
	}
	return nil, fmt.Errorf("audio has no data chunk; %s", wantFormat)
}

func checkFormat(f []byte) error {
	if len(f) < 16 {
		return fmt.Errorf("audio fmt chunk is %d bytes, want at least 16", len(f))
	}
	format := binary.LittleEndian.Uint16(f[0:2])
	channels := binary.LittleEndian.Uint16(f[2:4])
	rate := binary.LittleEndian.Uint32(f[4:8])
	byteRate := binary.LittleEndian.Uint32(f[8:12])
	align := binary.LittleEndian.Uint16(f[12:14])
	bits := binary.LittleEndian.Uint16(f[14:16])
	switch {
	case format != 1:
		return fmt.Errorf("audio format is %d, not PCM (1); %s", format, wantFormat)
	case channels != 1:
		return fmt.Errorf("audio has %d channels; %s", channels, wantFormat)
	case rate != SampleRate:
		return fmt.Errorf("audio is %d Hz; %s", rate, wantFormat)
	case bits != 16:
		return fmt.Errorf("audio is %d-bit; %s", bits, wantFormat)
	case align != 2 || byteRate != SampleRate*2:
		return fmt.Errorf("audio fmt chunk has block align %d and byte rate %d, want 2 and %d", align, byteRate, SampleRate*2)
	}
	return nil
}
