package runtime

import (
	"bufio"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

func TestExecAttachmentEOFPreservesFinalOutput(t *testing.T) {
	reader, writer := net.Pipe()
	attachment := newExecAttachment(nil, "exec", client.HijackedResponse{Conn: reader, Reader: bufio.NewReader(reader)})
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
	defer writer.Close()
	attachment := newExecAttachment(nil, "exec", client.HijackedResponse{Conn: reader, Reader: bufio.NewReader(reader)})
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
