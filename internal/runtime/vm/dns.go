package vm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"slices"
	"time"
)

const (
	dnsPort = 53
	// dnsTimeout bounds each upstream try, so a dead first resolver leaves
	// time for the next before the guest's own 5 s retry.
	dnsTimeout = 2 * time.Second
	// dnsMaxServers is how many resolv.conf servers glibc reads.
	dnsMaxServers = 3
	dnsMaxQueries = 1024
	dnsHeaderLen  = 12
)

// dnsRelay answers session VMs' DNS queries from the host's own resolvers,
// as Docker's embedded DNS server does for containers. A VM so resolves
// whatever its host resolves: through a loopback stub such as
// systemd-resolved's, a tailnet's or VPN's split DNS, and on a network that
// drops DNS to any server but its own.
type dnsRelay struct {
	udp       net.PacketConn
	tcp       net.Listener
	allowed   netip.Prefix
	upstreams func() []netip.AddrPort
	slots     chan struct{}
}

// listenDNS starts a relay on addr, at ports of the kernel's choosing so no
// DNS server of the host's holds them; the firewall sends the guests' port
// 53 there. It answers only clients in allowed.
func listenDNS(addr netip.Addr, allowed netip.Prefix, upstreams func() []netip.AddrPort) (*dnsRelay, error) {
	udp, err := net.ListenPacket("udp", netip.AddrPortFrom(addr, 0).String())
	if err != nil {
		return nil, fmt.Errorf("vm runtime: DNS relay: %w", err)
	}
	tcp, err := net.Listen("tcp", netip.AddrPortFrom(addr, 0).String())
	if err != nil {
		_ = udp.Close()
		return nil, fmt.Errorf("vm runtime: DNS relay: %w", err)
	}
	r := &dnsRelay{udp: udp, tcp: tcp, allowed: allowed, upstreams: upstreams, slots: make(chan struct{}, dnsMaxQueries)}
	go r.serveUDP()
	go r.serveTCP()
	return r, nil
}

func (r *dnsRelay) ports() (udp, tcp uint16) {
	return r.udp.LocalAddr().(*net.UDPAddr).AddrPort().Port(), r.tcp.Addr().(*net.TCPAddr).AddrPort().Port()
}

func (r *dnsRelay) close() {
	_ = r.udp.Close()
	_ = r.tcp.Close()
}

func (r *dnsRelay) admits(addr netip.AddrPort) bool {
	return r.allowed.Contains(addr.Addr().Unmap())
}

// take holds one of the relay's query slots; a full relay drops the query,
// which the guest's resolver retries.
func (r *dnsRelay) take() bool {
	select {
	case r.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (r *dnsRelay) serveUDP() {
	buf := make([]byte, 1<<16)
	for {
		n, from, err := r.udp.ReadFrom(buf)
		if errors.Is(err, net.ErrClosed) {
			return
		}
		client, ok := from.(*net.UDPAddr)
		if err != nil || !ok || n < dnsHeaderLen || !r.admits(client.AddrPort()) || !r.take() {
			continue
		}
		query := slices.Clone(buf[:n])
		go func() {
			defer func() { <-r.slots }()
			if reply := r.exchange("udp", query); reply != nil {
				_, _ = r.udp.WriteTo(reply, client)
			}
		}()
	}
}

func (r *dnsRelay) serveTCP() {
	for {
		c, err := r.tcp.Accept()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			slog.Warn("vm runtime: DNS relay accept", "error", err)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		client, ok := c.RemoteAddr().(*net.TCPAddr)
		if !ok || !r.admits(client.AddrPort()) || !r.take() {
			_ = c.Close()
			continue
		}
		go func() {
			defer func() { <-r.slots }()
			defer func() { _ = c.Close() }()
			for {
				_ = c.SetDeadline(time.Now().Add(4 * dnsTimeout))
				query, err := readDNSMessage(c)
				if err != nil {
					return
				}
				reply := r.exchange("tcp", query)
				if reply == nil || writeDNSMessage(c, reply) != nil {
					return
				}
			}
		}()
	}
}

// exchange asks each upstream in turn and returns the first answer, or nil.
func (r *dnsRelay) exchange(network string, query []byte) []byte {
	for _, server := range r.upstreams() {
		reply, err := exchangeWith(network, server, query)
		if err == nil {
			return reply
		}
		slog.Debug("vm runtime: DNS relay upstream", "server", server, "network", network, "error", err)
	}
	return nil
}

func exchangeWith(network string, server netip.AddrPort, query []byte) ([]byte, error) {
	c, err := net.DialTimeout(network, server.String(), dnsTimeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(dnsTimeout))
	if network == "tcp" {
		if err = writeDNSMessage(c, query); err != nil {
			return nil, err
		}
		return readDNSMessage(c)
	}
	if _, err = c.Write(query); err != nil {
		return nil, err
	}
	buf := make([]byte, 1<<16)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return nil, err
		}
		if n >= dnsHeaderLen && buf[0] == query[0] && buf[1] == query[1] {
			return buf[:n], nil
		}
	}
}

// readDNSMessage reads one message of DNS over TCP, which prefixes each with
// its length (RFC 1035 4.2.2).
func readDNSMessage(c io.Reader) ([]byte, error) {
	var size [2]byte
	if _, err := io.ReadFull(c, size[:]); err != nil {
		return nil, err
	}
	msg := make([]byte, binary.BigEndian.Uint16(size[:]))
	if _, err := io.ReadFull(c, msg); err != nil {
		return nil, err
	}
	if len(msg) < dnsHeaderLen {
		return nil, errors.New("short DNS message")
	}
	return msg, nil
}

func writeDNSMessage(c io.Writer, msg []byte) error {
	_, err := c.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(msg))), msg...))
	return err
}

// hostResolvers are the servers the host itself queries, read at each query
// so a changed resolver applies at once. A loopback stub counts: the relay
// runs on the host.
func hostResolvers() []netip.AddrPort {
	var servers []netip.Addr
	if f, err := os.Open("/etc/resolv.conf"); err == nil {
		servers = nameservers(f)
		_ = f.Close()
	}
	if len(servers) == 0 {
		servers = dockerDNS
	}
	out := make([]netip.AddrPort, 0, dnsMaxServers)
	for _, s := range servers[:min(len(servers), dnsMaxServers)] {
		out = append(out, netip.AddrPortFrom(s, dnsPort))
	}
	return out
}
