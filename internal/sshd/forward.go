package sshd

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

type directTCPIPPayload struct {
	DestHost string
	DestPort uint32
	OrigHost string
	OrigPort uint32
}

func (s *Server) handleDirectTCPIP(ctx context.Context, member domain.MemberID, nc ssh.NewChannel) {
	var payload directTCPIPPayload
	if ssh.Unmarshal(nc.ExtraData(), &payload) != nil {
		rejectDirectTCPIP(nc, ssh.Prohibited, "invalid port forwarding payload")
		return
	}
	if !strings.HasPrefix(payload.DestHost, "run:") {
		rejectDirectTCPIP(nc, ssh.Prohibited, "port forwarding targets must be run:<run-id>")
		return
	}
	runID := domain.RunID(strings.TrimPrefix(payload.DestHost, "run:"))
	if runID == "" {
		rejectDirectTCPIP(nc, ssh.Prohibited, "run not found")
		return
	}
	if payload.DestPort == 0 || payload.DestPort > 65535 {
		rejectDirectTCPIP(nc, ssh.Prohibited, "destination port must be between 1 and 65535")
		return
	}
	if err := checkSteer(ctx, s.cfg.Store, member, runID); err != nil {
		reason := err.Error()
		if errors.Is(err, store.ErrNotFound) {
			reason = "run not found"
		}
		rejectDirectTCPIP(nc, ssh.Prohibited, reason)
		return
	}
	addr, err := s.cfg.Runs.ContainerAddr(ctx, runID)
	if err != nil || addr == "" {
		rejectDirectTCPIP(nc, ssh.Prohibited, "run has no live container")
		return
	}
	tcpConn, err := net.DialTimeout("tcp", net.JoinHostPort(addr, strconv.Itoa(int(payload.DestPort))), 10*time.Second)
	if err != nil {
		rejectDirectTCPIP(nc, ssh.ConnectionFailed, err.Error())
		return
	}
	tcp, ok := tcpConn.(*net.TCPConn)
	if !ok {
		_ = tcpConn.Close()
		rejectDirectTCPIP(nc, ssh.ConnectionFailed, "forwarding connection is not TCP")
		return
	}

	ch, reqs, err := nc.Accept()
	if err != nil {
		_ = tcp.Close()
		return
	}
	s.spawn(func() { ssh.DiscardRequests(reqs) })
	proxyDirectTCPIP(ctx, tcp, ch)
	_ = tcp.Close()
	_ = ch.Close()
}

func rejectDirectTCPIP(nc ssh.NewChannel, reason ssh.RejectionReason, message string) {
	_ = nc.Reject(reason, message)
}

func proxyDirectTCPIP(ctx context.Context, tcp *net.TCPConn, ch ssh.Channel) {
	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() {
			_ = tcp.Close()
			_ = ch.Close()
		})
	}

	watchDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			closeBoth()
		case <-watchDone:
		}
	}()
	defer close(watchDone)

	copyDone := make(chan error, 2)
	go func() {
		_, err := io.Copy(tcp, ch)
		if err == nil {
			err = tcp.CloseWrite()
		}
		if err != nil {
			closeBoth()
		}
		copyDone <- err
	}()
	go func() {
		_, err := io.Copy(ch, tcp)
		if err == nil {
			err = ch.CloseWrite()
		}
		if err != nil {
			closeBoth()
		}
		copyDone <- err
	}()

	for range 2 {
		if err := <-copyDone; err != nil {
			closeBoth()
		}
	}
	closeBoth()
}
