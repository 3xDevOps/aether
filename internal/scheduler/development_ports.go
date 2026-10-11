package scheduler

import (
	"cmp"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// listeningScript prints the LISTEN rows of /proc/net/tcp and /proc/net/tcp6,
// a blank line, then `ls -l` of every visible process's exe link and of each
// descriptor that is one of those sockets, under a "/proc/<pid>/fd:" heading.
// It exits 1 with grep's own message when the tables cannot be read. Owners
// are best effort: the run's user cannot read another user's descriptors.
const listeningScript = `set -- /proc/net/tcp
[ -e /proc/net/tcp6 ] && set -- "$@" /proc/net/tcp6
listening=$(grep -h ' 0A ' "$@")
case $? in
0) ;;
1) exit 0 ;;
*) exit 1 ;;
esac
printf '%s\n\n' "$listening"
set --
while read -r _ _ _ _ _ _ _ _ _ inode _
do
	set -- "$@" -e "socket:[$inode]"
done <<EOF
$listening
EOF
ls -l /proc/[0-9]*/exe /proc/[0-9]*/fd 2>/dev/null | grep -F -e '/exe -> ' -e '/fd:' "$@"
exit 0`

const listeningBound = 10 * time.Second

var (
	processExe    = regexp.MustCompile(`/proc/(\d+)/exe -> (.+)$`)
	processFDs    = regexp.MustCompile(`^/proc/(\d+)/fd:$`)
	processSocket = regexp.MustCompile(` -> socket:\[(\d+)\]$`)
)

func (s *Scheduler) developmentPorts(ctx context.Context, id domain.RunID, raw json.RawMessage) (any, error) {
	var req protocol.DevPortListParams
	if err := decodeDevelopment(raw, &req); err != nil {
		return nil, err
	}
	live, err := s.ResolveLiveRun(ctx, id, false)
	if err != nil {
		return nil, err
	}
	ports, err := s.listeningPorts(ctx, live.ContainerID)
	if err != nil {
		return nil, err
	}
	return protocol.DevPortListResult{Ports: ports}, nil
}

func (s *Scheduler) listeningPorts(ctx context.Context, cid runtime.ID) ([]protocol.DevPort, error) {
	ctx, cancel := context.WithTimeout(ctx, listeningBound)
	defer cancel()
	code, stdout, stderr, err := s.cfg.Runtime.Exec(ctx, cid, []string{"/bin/sh", "-c", listeningScript}, "")
	if err != nil {
		return nil, fmt.Errorf("read the run's listening ports: %w", err)
	}
	if code != 0 {
		return nil, fmt.Errorf("read the run's listening ports: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	return parseListeningPorts(stdout), nil
}

type listeningSocket struct {
	addr  netip.Addr
	port  int
	inode string
}

// parseListeningPorts reads listeningScript's output into one entry per port.
// A port bound more than once keeps its widest binding: every interface
// before one address before loopback, IPv4 before IPv6.
func parseListeningPorts(out string) []protocol.DevPort {
	tables, owners, _ := strings.Cut(out, "\n\n")
	var sockets []listeningSocket
	for line := range strings.SplitSeq(tables, "\n") {
		if socket, ok := parseListeningSocket(line); ok {
			sockets = append(sockets, socket)
		}
	}
	slices.SortFunc(sockets, func(a, b listeningSocket) int {
		return cmp.Or(cmp.Compare(a.port, b.port), cmp.Compare(reach(a.addr), reach(b.addr)), cmp.Compare(a.addr.BitLen(), b.addr.BitLen()))
	})
	sockets = slices.CompactFunc(sockets, func(a, b listeningSocket) bool { return a.port == b.port })

	names := socketOwners(owners)
	ports := make([]protocol.DevPort, 0, len(sockets))
	for _, socket := range sockets {
		ports = append(ports, protocol.DevPort{Port: socket.port, Address: socket.addr.String(), Process: names[socket.inode]})
	}
	return ports
}

// parseListeningSocket reads one /proc/net/tcp or tcp6 row. The kernel prints
// an address as 32-bit words in host byte order, which is little-endian on
// every platform the server runs on.
func parseListeningSocket(line string) (listeningSocket, bool) {
	fields := strings.Fields(line)
	if len(fields) < 10 || fields[3] != "0A" {
		return listeningSocket{}, false
	}
	hexAddr, hexPort, ok := strings.Cut(fields[1], ":")
	if !ok {
		return listeningSocket{}, false
	}
	port, err := strconv.ParseUint(hexPort, 16, 16)
	if err != nil || port == 0 {
		return listeningSocket{}, false
	}
	raw, err := hex.DecodeString(hexAddr)
	if err != nil || len(raw)%4 != 0 {
		return listeningSocket{}, false
	}
	for word := 0; word < len(raw); word += 4 {
		slices.Reverse(raw[word : word+4])
	}
	addr, ok := netip.AddrFromSlice(raw)
	if !ok {
		return listeningSocket{}, false
	}
	return listeningSocket{addr: addr.Unmap(), port: int(port), inode: fields[9]}, true
}

func reach(addr netip.Addr) int {
	switch {
	case addr.IsUnspecified():
		return 0
	case addr.IsLoopback():
		return 2
	}
	return 1
}

// socketOwners maps a socket inode to the executable name of the lowest pid
// holding it: a server that forks workers is named by its parent.
func socketOwners(listing string) map[string]string {
	exes := make(map[int]string)
	holders := make(map[string]int)
	pid := 0
	for line := range strings.SplitSeq(listing, "\n") {
		if match := processExe.FindStringSubmatch(line); match != nil {
			if owner, err := strconv.Atoi(match[1]); err == nil {
				exes[owner] = path.Base(strings.TrimSuffix(match[2], " (deleted)"))
			}
		} else if match := processFDs.FindStringSubmatch(line); match != nil {
			pid, _ = strconv.Atoi(match[1])
		} else if match := processSocket.FindStringSubmatch(line); match != nil && pid != 0 {
			if held, ok := holders[match[1]]; !ok || pid < held {
				holders[match[1]] = pid
			}
		}
	}
	names := make(map[string]string, len(holders))
	for inode, holder := range holders {
		names[inode] = exes[holder]
	}
	return names
}
