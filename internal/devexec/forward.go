package devexec

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
)

// Forward and SFTP are the helper entry points behind a member's SSH
// connection to a run. Both serve one stream on their own stdio.
const (
	Forward = "forward"
	SFTP    = "sftp"
)

const forwardDialTimeout = 10 * time.Second

// Stream is a byte stream as the net.Conn x/crypto/ssh asks for. Deadlines
// are not supported.
type Stream struct {
	io.Reader
	io.Writer
	io.Closer
}

type streamAddr struct{}

func (streamAddr) Network() string { return "stream" }
func (streamAddr) String() string  { return "stream" }

func (Stream) LocalAddr() net.Addr              { return streamAddr{} }
func (Stream) RemoteAddr() net.Addr             { return streamAddr{} }
func (Stream) SetDeadline(time.Time) error      { return nil }
func (Stream) SetReadDeadline(time.Time) error  { return nil }
func (Stream) SetWriteDeadline(time.Time) error { return nil }

// LoopbackAddrs returns the addresses a forward to host dials, in order. Only
// loopback is a destination, so a forward can reach nothing the namespace it
// is dialed from does not own.
func LoopbackAddrs(host string) ([]string, error) {
	if host == "localhost" {
		return []string{"127.0.0.1", "::1"}, nil
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.IsLoopback() && ip.Zone() == "" {
		return []string{ip.String()}, nil
	}
	return nil, fmt.Errorf("port forwarding reaches only the run's own loopback: forward to localhost, not %q", host)
}

// ServeForward serves the SSH channel protocol on conn and relays each
// direct-tcpip channel to this network namespace's loopback. The caller owns
// both ends of conn, so the connection carries no authentication of its own.
func ServeForward(conn net.Conn) error {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return err
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return err
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		go forwardChannel(nc)
	}
	return sconn.Wait()
}

func forwardChannel(nc ssh.NewChannel) {
	if nc.ChannelType() != "direct-tcpip" {
		_ = nc.Reject(ssh.UnknownChannelType, "unsupported channel type")
		return
	}
	var payload struct {
		DestHost string
		DestPort uint32
		OrigHost string
		OrigPort uint32
	}
	if ssh.Unmarshal(nc.ExtraData(), &payload) != nil || payload.DestPort == 0 || payload.DestPort > 65535 {
		_ = nc.Reject(ssh.Prohibited, "invalid port forwarding payload")
		return
	}
	addrs, err := LoopbackAddrs(payload.DestHost)
	if err != nil {
		_ = nc.Reject(ssh.Prohibited, err.Error())
		return
	}
	var conn net.Conn
	for _, addr := range addrs {
		conn, err = net.DialTimeout("tcp", net.JoinHostPort(addr, strconv.Itoa(int(payload.DestPort))), forwardDialTimeout)
		if err == nil {
			break
		}
	}
	if err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	tcp := conn.(*net.TCPConn)
	ch, reqs, err := nc.Accept()
	if err != nil {
		_ = tcp.Close()
		return
	}
	// Requests end when the peer closes the whole channel, which a read on
	// the idle TCP side would otherwise never notice.
	go func() {
		ssh.DiscardRequests(reqs)
		_ = tcp.Close()
	}()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(tcp, ch)
		_ = tcp.CloseWrite()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(ch, tcp)
		_ = ch.CloseWrite()
		done <- struct{}{}
	}()
	<-done
	<-done
	_ = tcp.Close()
	_ = ch.Close()
}
