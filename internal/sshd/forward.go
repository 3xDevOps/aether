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
	if payload.DestPort == 0 || payload.DestPort > 65535 {
		rejectDirectTCPIP(nc, ssh.Prohibited, "destination port must be between 1 and 65535")
		return
	}
	if err := ctx.Err(); err != nil {
		return
	}
	var (
		addr           string
		terminalTarget bool
		runID          domain.RunID
	)
	switch {
	case payload.DestHost == "terminal":
		terminalTarget = true
		if err := s.checkMember(ctx, member); err != nil {
			rejectDirectTCPIP(nc, ssh.Prohibited, rpcError(err).Message)
			return
		}
		var err error
		addr, err = s.cfg.Runs.TerminalContainerAddr(ctx, member)
		if err != nil || addr == "" {
			rejectDirectTCPIP(nc, ssh.Prohibited, "environment terminal is not running")
			return
		}
	case strings.HasPrefix(payload.DestHost, "run:") && strings.TrimPrefix(payload.DestHost, "run:") != "":
		runID = domain.RunID(strings.TrimPrefix(payload.DestHost, "run:"))
		if err := checkSteer(ctx, s.cfg.Store, member, runID); err != nil {
			reason := err.Error()
			if errors.Is(err, store.ErrNotFound) {
				reason = "run not found"
			}
			rejectDirectTCPIP(nc, ssh.Prohibited, reason)
			return
		}
		var err error
		addr, err = s.cfg.Runs.ContainerAddr(ctx, runID)
		if err != nil || addr == "" {
			rejectDirectTCPIP(nc, ssh.Prohibited, "run has no live container")
			return
		}
	default:
		rejectDirectTCPIP(nc, ssh.Prohibited, "port forwarding targets must be run:<run-id> or terminal")
		return
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	tcpConn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(addr, strconv.Itoa(int(payload.DestPort))))
	if err != nil {
		rejectDirectTCPIP(nc, ssh.ConnectionFailed, err.Error())
		return
	}
	if ctx.Err() != nil {
		_ = tcpConn.Close()
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
	channelCtx, cancelChannel := context.WithCancel(ctx)
	defer cancelChannel()
	// Request EOF marks a full channel close, not a client half-close.
	// Keep consuming requests while the proxy is live so SSH cannot stall,
	// and cancel the proxy once the peer has closed the whole channel.
	s.spawn(func() {
		for req := range reqs {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
		cancelChannel()
	})
	s.spawn(func() {
		s.revokeDirectTCPIPOnPolicyChange(channelCtx, cancelChannel, member, runID, terminalTarget)
	})
	proxyDirectTCPIP(channelCtx, tcp, ch)
	_ = tcp.Close()
	_ = ch.Close()
}

// revokeDirectTCPIPOnPolicyChange re-runs the target authorization while a
// direct-tcpip proxy is live. The initial gate is a snapshot: without this,
// a member removed or left pending keeps a terminal tunnel open, and a
// member who loses Steer keeps a run tunnel open until disconnect.
func (s *Server) revokeDirectTCPIPOnPolicyChange(ctx context.Context, cancel context.CancelFunc, member domain.MemberID, run domain.RunID, terminal bool) {
	ticker := time.NewTicker(s.cfg.revalidateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if terminal {
				if s.checkMember(ctx, member) == nil {
					continue
				}
			} else if checkSteer(ctx, s.cfg.Store, member, run) == nil {
				continue
			}
			cancel()
			return
		}
	}
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
