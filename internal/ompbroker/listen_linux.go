package ompbroker

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// listenedBy reports whether a socket of uid listens on 127.0.0.1:port, as
// /proc/net/tcp lists it: the address in host byte order, little-endian on
// the x86-64 hosts session VMs run on, and state 0A for LISTEN.
func listenedBy(port, uid int) bool {
	f, err := os.Open("/proc/net/tcp")
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	local := fmt.Sprintf("0100007F:%04X", port)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 7 && fields[1] == local && fields[3] == "0A" && fields[7] == strconv.Itoa(uid) {
			return true
		}
	}
	return false
}
