package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
)

// resolvConfEnv names a resolv.conf the pure-Go resolver reads instead of
// /etc/resolv.conf. Stock Android has no /etc/resolv.conf, so without it
// every lookup falls back to 127.0.0.1:53 and fails.
const resolvConfEnv = "AETHER_RESOLV_CONF"

// useResolvConfEnv installs the resolver only when /etc/resolv.conf is
// missing, so a host that has one keeps its own configuration.
func useResolvConfEnv() {
	path := os.Getenv(resolvConfEnv)
	if path == "" {
		return
	}
	if _, err := os.Stat("/etc/resolv.conf"); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	net.DefaultResolver = newFileResolver(path, 53)
}

// newFileResolver dials the nameservers listed in path, reading the file at
// each dial because the app rewrites it when the phone changes networks.
// Go retries a failed query on its next configured server; rotating here
// makes those retries reach different servers.
func newFileResolver(path string, port uint16) *net.Resolver {
	var next atomic.Uint64
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			servers, err := readNameservers(path)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", resolvConfEnv, path, err)
			}
			server := servers[(next.Add(1)-1)%uint64(len(servers))]
			var d net.Dialer
			return d.DialContext(ctx, network, netip.AddrPortFrom(server, port).String())
		},
	}
}

func readNameservers(path string) ([]netip.Addr, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	servers := parseNameservers(data)
	if len(servers) == 0 {
		return nil, errors.New("no nameserver line")
	}
	return servers, nil
}

// parseNameservers returns the address of each valid `nameserver <ip>` line.
func parseNameservers(data []byte) []netip.Addr {
	var servers []netip.Addr
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		if addr, err := netip.ParseAddr(fields[1]); err == nil {
			servers = append(servers, addr)
		}
	}
	return servers
}
