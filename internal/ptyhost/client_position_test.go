package ptyhost

import (
	"bytes"
	"testing"
)

type typedResumeConn struct {
	bytes.Buffer
	position TerminalPosition
	resumed  bool
}

func (c *typedResumeConn) SetTerminalPosition(position TerminalPosition, resumed bool) {
	c.position = position
	c.resumed = resumed
}

type legacyResumeConn struct {
	bytes.Buffer
	cursor   uint64
	resumed  bool
	resumeID string
}

func (c *legacyResumeConn) SetResume(cursor uint64, resumed bool, resumeID string) {
	c.cursor = cursor
	c.resumed = resumed
	c.resumeID = resumeID
}

func TestClientReportsTypedPositionWithLegacyFallback(t *testing.T) {
	want := TerminalPosition{Epoch: "epoch", Sequence: 73}
	typed := &typedResumeConn{}
	client := newClient(typed, AttachClient{})
	client.position = want
	client.resumed = true
	client.tellResume("ignored")
	if typed.position != want || !typed.resumed {
		t.Fatalf("typed resume = %+v resumed=%v, want %+v resumed", typed.position, typed.resumed, want)
	}

	legacy := &legacyResumeConn{}
	client = newClient(legacy, AttachClient{})
	client.position = want
	client.resumed = true
	client.tellResume("ignored")
	if legacy.cursor != uint64(want.Sequence) || legacy.resumeID != string(want.Epoch) || !legacy.resumed {
		t.Fatalf("legacy resume = cursor %d id %q resumed=%v", legacy.cursor, legacy.resumeID, legacy.resumed)
	}
}

func TestNewClientPrefersAtomicPosition(t *testing.T) {
	want := TerminalPosition{Epoch: "typed", Sequence: 42}
	client := newClient(&bytes.Buffer{}, AttachClient{
		Position: want,
		ResumeID: "legacy",
		Cursor:   99,
	})
	if client.position != want {
		t.Fatalf("position = %+v, want %+v", client.position, want)
	}
}
