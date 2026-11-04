package dictation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	// maxQueued bounds the requests waiting behind the one transcribing.
	maxQueued         = 4
	transcribeTimeout = 3 * time.Minute
)

// Service transcribes one clip at a time server-wide so dictation never
// takes more than one engine's threads from the agents. Audio lives only
// in the request's memory: nothing here writes, logs or keeps it.
type Service struct {
	dir    string
	engine Transcriber
	slot   chan struct{}

	mu      sync.Mutex
	queued  int
	members map[domain.MemberID]bool
}

// New serves the install in dir with engine.
func New(dir string, engine Transcriber) *Service {
	return &Service{dir: dir, engine: engine, slot: make(chan struct{}, 1), members: map[domain.MemberID]bool{}}
}

// Installed reports whether dir holds a complete install.
func (s *Service) Installed() bool {
	_, err := ReadManifest(s.dir)
	return err == nil
}

// Transcribe returns the text of wav, a clip ParseWAV accepts, with hints
// applied. Errors are *protocol.Error.
func (s *Service) Transcribe(ctx context.Context, member domain.MemberID, wav []byte, hints []string) (string, error) {
	if err := checkHints(hints); err != nil {
		return "", &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()}
	}
	pcm, err := ParseWAV(wav)
	if err != nil {
		return "", &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()}
	}
	if _, err = ReadManifest(s.dir); err != nil {
		return "", unavailable(err.Error())
	}
	release, err := s.admit(ctx, member)
	if err != nil {
		return "", err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, transcribeTimeout)
	defer cancel()
	text, err := s.engine.Transcribe(ctx, pcm)
	if err != nil {
		return "", err
	}
	return ApplyHints(text, hints), nil
}

// admit holds the one transcription slot for member, waiting behind at
// most maxQueued others, and refuses a member who already has a request
// in flight or waiting.
func (s *Service) admit(ctx context.Context, member domain.MemberID) (func(), error) {
	s.mu.Lock()
	if s.members[member] {
		s.mu.Unlock()
		return nil, unavailable("dictation is busy: you already have a dictation transcribing or queued; wait for it to finish")
	}
	select {
	case s.slot <- struct{}{}:
	default:
		if s.queued >= maxQueued {
			n := s.queued
			s.mu.Unlock()
			return nil, unavailable(fmt.Sprintf("dictation is busy: %d requests queued; try again", n))
		}
		s.queued++
		s.members[member] = true
		s.mu.Unlock()
		select {
		case s.slot <- struct{}{}:
		case <-ctx.Done():
			s.mu.Lock()
			s.queued--
			delete(s.members, member)
			s.mu.Unlock()
			return nil, unavailable("dictation request ended while queued: " + ctx.Err().Error())
		}
		s.mu.Lock()
		s.queued--
	}
	s.members[member] = true
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.members, member)
		s.mu.Unlock()
		<-s.slot
	}, nil
}

// Close stops the engine.
func (s *Service) Close() error { return s.engine.Close() }

func unavailable(msg string) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeUnavailable, Message: msg}
}
