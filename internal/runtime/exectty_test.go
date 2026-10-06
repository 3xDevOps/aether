package runtime

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

func TestExecAttachmentEOFPreservesFinalOutput(t *testing.T) {
	reader, writer := net.Pipe()
	attachment := newExecAttachment(nil, "exec", true, client.HijackedResponse{Conn: reader, Reader: bufio.NewReader(reader)})
	t.Cleanup(func() { _ = attachment.Close() })
	go func() {
		_, _ = io.WriteString(writer, "final command output\n")
		_ = writer.Close()
	}()
	select {
	case <-attachment.done:
	case <-time.After(5 * time.Second):
		t.Fatal("attachment did not observe EOF")
	}
	if attachment.connected() {
		t.Fatal("EOF must end transport availability")
	}
	execution := &dockerManagedExec{attachment: attachment}
	att := execution.Attachment()
	if att == nil {
		t.Fatal("a fast command's buffered output became unavailable before its first read")
	}
	output, err := io.ReadAll(att.Stdout())
	if err != nil || string(output) != "final command output\n" {
		t.Fatalf("output = %q, %v", output, err)
	}
	if err := execution.Detach(); err != nil || execution.Attachment() != nil {
		t.Fatalf("detach did not release the attachment: %v", err)
	}
}

func TestExecAttachmentConcurrentCloseUnblocksRead(t *testing.T) {
	reader, writer := net.Pipe()
	defer func() { _ = writer.Close() }()
	attachment := newExecAttachment(nil, "exec", true, client.HijackedResponse{Conn: reader, Reader: bufio.NewReader(reader)})
	readDone := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(attachment.Stdout())
		readDone <- err
	}()
	var closers sync.WaitGroup
	for range 8 {
		closers.Go(func() { _ = attachment.Close() })
	}
	closers.Wait()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("explicit close must end a blocked read with EOF: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close left a stdout reader blocked")
	}
	if _, err := attachment.Stdin().Write([]byte("input after detach")); err == nil {
		t.Fatal("detached transport still accepted input")
	}
}

func TestPipeExecStdoutIsLossless(t *testing.T) {
	reader, writer := net.Pipe()
	attachment := newExecAttachment(nil, "exec", false, client.HijackedResponse{Conn: reader, Reader: bufio.NewReader(reader)})
	t.Cleanup(func() { _ = attachment.Close() })
	const total = maxStreamBuffer + 3<<20
	go func() {
		// One Docker multiplexed stdout frame: stream 1, then the length.
		frame := make([]byte, 8+32<<10)
		frame[0] = byte(stdcopy.Stdout)
		binary.BigEndian.PutUint32(frame[4:8], 32<<10)
		for sent := 0; sent < total; sent += 32 << 10 {
			if _, err := writer.Write(frame); err != nil {
				return
			}
		}
		_ = writer.Close()
	}()
	// Let the producer run far past the cap before anything is read.
	time.Sleep(200 * time.Millisecond)
	n, err := io.Copy(io.Discard, attachment.Stdout())
	if err != nil || n != total {
		t.Fatalf("read %d bytes (%v), want %d", n, err, total)
	}
}
