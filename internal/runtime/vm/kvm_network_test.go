//go:build kvm

package vm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/devexec"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/egress"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const probePath = "/opt/aether-vmtest"

// TestKVMNetProbe is what TestKVMNetwork runs inside guests and containers,
// from its own binary, which they mount at probePath.
func TestKVMNetProbe(t *testing.T) {
	args := strings.Fields(os.Getenv("AETHER_VM_TEST_PROBE"))
	if len(args) == 0 {
		t.Skip("runs inside a guest only")
	}
	switch args[0] {
	case "listen":
		l, err := net.Listen("tcp", args[1])
		if err != nil {
			t.Fatal(err)
		}
		name, _ := os.Hostname()
		for c, err := l.Accept(); err == nil; c, err = l.Accept() {
			_, _ = fmt.Fprintln(c, "hello from", name)
			_ = c.Close()
		}
	case "dial":
		fmt.Println(dial(args[1]))
	case "connect":
		c, err := net.DialTimeout("tcp", args[1], 3*time.Second)
		if err != nil {
			fmt.Println("error:", err)
			return
		}
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		_, _ = fmt.Fprintf(c, "CONNECT %[1]s HTTP/1.1\r\nHost: %[1]s\r\n\r\n", args[2])
		status, err := bufio.NewReader(c).ReadString('\n')
		if err != nil {
			fmt.Println("error:", err)
			return
		}
		fmt.Println("status:", strings.TrimSpace(status))
	case "udp":
		// IP_TRANSPARENT lets root send from an address the guest does not hold.
		d := net.Dialer{LocalAddr: &net.UDPAddr{IP: net.ParseIP(args[1])}, Control: func(_, _ string, rc syscall.RawConn) error {
			var optErr error
			if err := rc.Control(func(fd uintptr) { optErr = unix.SetsockoptInt(int(fd), unix.SOL_IP, unix.IP_TRANSPARENT, 1) }); err != nil {
				return err
			}
			return optErr
		}}
		c, err := d.Dial("udp", args[2])
		if err == nil {
			_, err = c.Write([]byte(args[1]))
		}
		if err != nil {
			fmt.Println("error:", err)
			return
		}
		fmt.Println("sent: from", args[1])
	}
}

type probeStore map[domain.RunID]*domain.Workspace

func (s probeStore) GetRun(_ context.Context, id domain.RunID) (*domain.Run, error) {
	ws := s[id]
	if ws == nil {
		return nil, fmt.Errorf("no run %s", id)
	}
	return &domain.Run{ID: id, WorkspaceID: ws.ID}, nil
}

func (s probeStore) GetWorkspace(_ context.Context, id domain.WorkspaceID) (*domain.Workspace, error) {
	for _, ws := range s {
		if ws.ID == id {
			return ws, nil
		}
	}
	return nil, fmt.Errorf("no workspace %s", id)
}

type probeBus struct{}

func (probeBus) Publish(_ context.Context, e events.Event) (events.Event, error) { return e, nil }

func probeVM(t *testing.T, e *engine, key string, egressOnly bool) string {
	t.Helper()
	t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec := runtime.Spec{
		Image: "docker.io/library/ubuntu:24.04", CreationKey: key, Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
		Env: map[string]string{"AETHER_RUN_ID": key}, CPULimit: 1, MemoryLimitBytes: 512 << 20, Egress: egressOnly,
		Mounts: []runtime.Mount{
			{HostPath: self, ContainerPath: probePath, ReadOnly: true},
			{HostPath: os.Getenv("AETHER_VM_TEST_SERVER"), ContainerPath: coordtransport.CLIPath, ReadOnly: true},
		},
	}
	if _, err = e.create(t.Context(), spec.CreationKey, spec, options{}); err != nil {
		t.Fatal(err)
	}
	if err = e.start(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	listen := "AETHER_VM_TEST_PROBE='listen :8080' setsid " + probePath + " -test.run '^TestKVMNetProbe$' </dev/null >/dev/null 2>&1 &" +
		"AETHER_VM_TEST_PROBE='listen 127.0.0.1:8081' setsid " + probePath + " -test.run '^TestKVMNetProbe$' </dev/null >/dev/null 2>&1 & sleep 1"
	if out, code := vmRun(t, e, key, listen); code != 0 {
		t.Fatalf("start the listeners in %s: %s", key, out)
	}
	ip, err := e.containerIP(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	return ip
}

func probe(t *testing.T, e *engine, key, args string) string {
	t.Helper()
	out, _ := vmRun(t, e, key, "AETHER_VM_TEST_PROBE='"+args+"' "+probePath+" -test.run '^TestKVMNetProbe$' | grep -E '^(connected|error|status|sent):'")
	return strings.TrimSpace(out)
}

// dial reports whether a TCP connection to addr opened, and the first line
// its server sent, if any.
func dial(addr string) string {
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return "error: " + err.Error()
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, _ := bufio.NewReader(c).ReadString('\n')
	return "connected: " + strings.TrimSpace(line)
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	var stderr strings.Builder
	cmd := exec.CommandContext(t.Context(), "docker", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker %s: %v: %s%s", strings.Join(args, " "), err, out, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

// TestKVMNetwork checks what docs/environments.md promises a session VM's
// network: open VMs reach the host and the internet but not each other, a
// Docker container or the metadata service; a VM with egress control
// reaches only the proxy, which names its run; port forwards reach a dev
// server in either; and a new engine rebuilds the firewall.
func TestKVMNetwork(t *testing.T) {
	e := kvmEngine(t)
	if links, err := net.Interfaces(); err == nil {
		for _, l := range links {
			if strings.HasPrefix(l.Name, tapPrefix) {
				t.Fatalf("VM tap %s is up: this test replaces the host's VM firewall, so it needs a host where no other VM runs", l.Name)
			}
		}
	}
	t.Cleanup(func() {
		_ = exec.Command("nft", "delete", "table", "inet", firewallTable).Run()
		_ = exec.Command("ip", "link", "del", proxyLink).Run()
		for _, chain := range []string{"INPUT", "FORWARD"} {
			_ = exec.Command("iptables", "-D", chain, "-m", "mark", "--mark", "0x1000000/0x1000000", "-j", "ACCEPT").Run()
		}
	})
	ws := func(id, host string) *domain.Workspace {
		return &domain.Workspace{ID: domain.WorkspaceID(id), Environment: domain.WorkspaceEnvironment{Egress: domain.EgressPolicy{On: true, Hosts: []string{host}}}}
	}
	proxy := egress.New(&Runtime{vms: e}, probeStore{"run-vmnet-c": ws("ws-allows", "example.com"), "run-vmnet-d": ws("ws-denies", "example.org")}, probeBus{})
	if _, err := proxy.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proxy.Close() }()
	greet := func(addr string) {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Close() })
		go func() {
			for c, acceptErr := l.Accept(); acceptErr == nil; c, acceptErr = l.Accept() {
				_, _ = fmt.Fprintln(c, "hello from the host")
				_ = c.Close()
			}
		}()
	}
	hostPort := proxyAddr.String() + ":18080"
	greet(hostPort)
	hostUDP, err := net.ListenPacket("udp4", proxyAddr.String()+":18081")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hostUDP.Close() }()

	a, b := probeVM(t, e, "run-vmnet-a", false), probeVM(t, e, "run-vmnet-b", false)
	c, d := probeVM(t, e, "run-vmnet-c", true), probeVM(t, e, "run-vmnet-d", true)
	self, _ := os.Executable()
	container := docker(t, "run", "-d", "--rm", "-v", self+":"+probePath+":ro", "-e", "AETHER_VM_TEST_PROBE=listen :8080",
		"docker.io/library/ubuntu:24.04", probePath, "-test.run", "^TestKVMNetProbe$")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() })
	containerIP := docker(t, "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", container)
	time.Sleep(time.Second)

	expect := func(what, got string, want bool) {
		t.Helper()
		prefix := "error:"
		if want {
			prefix = "connected:"
		}
		if !strings.HasPrefix(got, prefix) {
			t.Errorf("%s: %q", what, got)
		} else {
			t.Logf("%s: %s", what, got)
		}
	}
	expect("the host reaches the Docker container", dial(containerIP+":8080"), true)
	targets := map[string]string{"another VM": b + ":8080", "a VM with egress control": c + ":8080", "a Docker container": containerIP + ":8080"}
	egressTargets := map[string]string{"the internet": "1.1.1.1:443", "another VM": a + ":8080", "the host": hostPort}
	for _, dns := range hostDNS() {
		egressTargets["the DNS server "+dns.String()] = dns.String() + ":53"
	}
	if got := dial("169.254.169.254:80"); strings.HasPrefix(got, "connected:") {
		targets["the metadata service"], egressTargets["the metadata service"] = "169.254.169.254:80", "169.254.169.254:80"
	} else {
		t.Logf("no metadata service answers this host (%s): its checks are left out", got)
	}
	// 169.254.169.253 is the AWS VPC DNS resolver: a link-local address other
	// than the metadata IPs that an open VM must not reach, unless it is the
	// host's own configured resolver (then it stays reachable on port 53).
	resolverIsDNS := false
	for _, dns := range hostDNS() {
		resolverIsDNS = resolverIsDNS || dns.String() == "169.254.169.253"
	}
	if got := dial("169.254.169.253:53"); strings.HasPrefix(got, "connected:") && !resolverIsDNS {
		targets["the VPC DNS resolver"], egressTargets["the VPC DNS resolver"] = "169.254.169.253:53", "169.254.169.253:53"
	} else {
		t.Logf("169.254.169.253 is not a reachable non-resolver link-local target here: %s", got)
	}

	expect("an open VM reaches the internet", probe(t, e, "run-vmnet-a", "dial example.com:80"), true)
	expect("an open VM reaches the host", probe(t, e, "run-vmnet-a", "dial "+hostPort), true)
	for what, target := range targets {
		expect("an open VM reaches "+what, probe(t, e, "run-vmnet-a", "dial "+target), false)
	}
	rec, err := e.load("run-vmnet-a")
	if err != nil {
		t.Fatal(err)
	}
	tap, err := net.InterfaceByName(tapName(rec.Net, false))
	if err != nil {
		t.Fatal(err)
	}
	addrs, _ := tap.Addrs()
	for _, addr := range addrs {
		if ip, _, _ := net.ParseCIDR(addr.String()); ip.To4() == nil && ip.IsLinkLocalUnicast() {
			greet("[" + ip.String() + "%" + tap.Name + "]:18080")
			expect("an open VM reaches the host over IPv6", probe(t, e, "run-vmnet-a", "dial ["+ip.String()+"%eth0]:18080"), false)
		}
	}
	got := probe(t, e, "run-vmnet-a", "connect "+proxyAddr.String()+":3128 example.com:443")
	if !strings.Contains(got, " 403 ") || !strings.Contains(got, "not a session with egress control on") {
		t.Errorf("the proxy served an open VM: %s", got)
	}

	for _, key := range []string{"run-vmnet-c", "run-vmnet-d"} {
		for what, target := range egressTargets {
			expect(key+" with egress control reaches "+what, probe(t, e, key, "dial "+target), false)
		}
	}
	if got = probe(t, e, "run-vmnet-c", "connect "+proxyAddr.String()+":3128 example.com:443"); !strings.Contains(got, " 200 ") {
		t.Errorf("the proxy refused run-vmnet-c a host its workspace allows: %s", got)
	}
	got = probe(t, e, "run-vmnet-d", "connect "+proxyAddr.String()+":3128 example.com:443")
	if !strings.Contains(got, " 403 ") || !strings.Contains(got, "--workspace ws-denies") {
		t.Errorf("the proxy did not hold run-vmnet-d to its own workspace's list: %s", got)
	}
	t.Logf("run-vmnet-d through the proxy: %s", got)

	// A default-deny INPUT, as ufw sets, still lets egress VMs reach the proxy.
	deny := []string{"INPUT", "-i", tapPrefix + "+", "-j", "DROP"}
	if out, denyErr := exec.Command("iptables", append([]string{"-A"}, deny...)...).CombinedOutput(); denyErr != nil {
		t.Fatalf("iptables -A %v: %v: %s", deny, denyErr, out)
	}
	t.Cleanup(func() { _ = exec.Command("iptables", append([]string{"-D"}, deny...)...).Run() })
	if got = probe(t, e, "run-vmnet-c", "connect "+proxyAddr.String()+":3128 example.com:443"); !strings.Contains(got, " 200 ") {
		t.Errorf("behind a default-deny INPUT the proxy refused run-vmnet-c: %s", got)
	}
	expect("behind a default-deny INPUT an open VM reaches the host", probe(t, e, "run-vmnet-a", "dial "+hostPort), false)
	if out, denyErr := exec.Command("iptables", append([]string{"-D"}, deny...)...).CombinedOutput(); denyErr != nil {
		t.Fatalf("iptables -D %v: %v: %s", deny, denyErr, out)
	}

	// sources sends a datagram as each address from run-vmnet-a and returns
	// the sources the host took them from.
	sources := func(as ...string) []string {
		for _, from := range as {
			if sent := probe(t, e, "run-vmnet-a", "udp "+from+" "+proxyAddr.String()+":18081"); !strings.HasPrefix(sent, "sent:") {
				t.Fatalf("run-vmnet-a cannot send as %s: %q", from, sent)
			}
		}
		var took []string
		buf := make([]byte, 64)
		_ = hostUDP.SetReadDeadline(time.Now().Add(2 * time.Second))
		for {
			_, from, readErr := hostUDP.ReadFrom(buf)
			if readErr != nil {
				return took
			}
			took = append(took, from.(*net.UDPAddr).IP.String())
		}
	}
	if took := sources(c, a); !slices.Equal(took, []string{a}) {
		t.Errorf("run-vmnet-a sent as %s and as itself; the host took datagrams from %v", c, took)
	} else {
		t.Logf("the host took only the datagram run-vmnet-a sent as itself, not the one it sent as %s", c)
	}

	got = docker(t, "run", "--rm", "-v", self+":"+probePath+":ro", "-e", "AETHER_VM_TEST_PROBE=dial "+b+":8080",
		"docker.io/library/ubuntu:24.04", probePath, "-test.run", "^TestKVMNetProbe$")
	expect("a Docker container reaches a VM", strings.Split(got, "\n")[0], false)

	for key, ip := range map[string]string{"run-vmnet-b": b, "run-vmnet-d": d} {
		for what, got := range map[string]string{"a port forward": dial(ip + ":8080"), "the forwarding relay": relayDial(t, e, key, "127.0.0.1:8081")} {
			expect(what+" reaches "+key, got, true)
			if !strings.HasSuffix(got, "hello from "+key) {
				t.Errorf("%s to %s reached another server: %s", what, key, got)
			}
		}
	}

	if err = exec.Command("nft", "delete", "table", "inet", firewallTable).Run(); err != nil {
		t.Fatal(err)
	}
	// Fail-closed holds where FORWARD drops by default and no other rule
	// accepts VM traffic.
	if filter, _ := exec.Command("iptables", "-S").Output(); strings.Contains(string(filter), "-P FORWARD DROP") && !strings.Contains(string(filter), tapPrefix) {
		expect("without the firewall an open VM reaches another VM", probe(t, e, "run-vmnet-a", "dial "+b+":8080"), false)
		expect("without the firewall an open VM reaches the internet", probe(t, e, "run-vmnet-a", "dial 1.1.1.1:443"), false)
	} else {
		t.Log("FORWARD does not drop by default or another rule accepts VM traffic: fail-closed not checked")
	}
	// The kernel's own source check, the larger of all and the tap's, is
	// loose on Ubuntu; only a strict one would drop the spoofed datagram
	// without the table.
	check := "0"
	for _, conf := range []string{"all", tap.Name} {
		value, _ := os.ReadFile("/proc/sys/net/ipv4/conf/" + conf + "/rp_filter")
		check = max(check, strings.TrimSpace(string(value)))
	}
	if check != "1" {
		if took := sources(c); !slices.Equal(took, []string{c}) {
			t.Errorf("without the firewall the host took datagrams sent as %s from %v, so the spoof check above proves nothing", c, took)
		}
	}
	restarted := &Runtime{vms: kvmEngine(t)}
	for range 2 {
		if _, err = restarted.EgressGateway(t.Context(), false); err != nil {
			t.Fatal(err)
		}
	}
	expect("after a new engine an open VM reaches another VM", probe(t, restarted.vms, "run-vmnet-a", "dial "+b+":8080"), false)
	if got = probe(t, restarted.vms, "run-vmnet-c", "connect "+proxyAddr.String()+":3128 example.com:443"); !strings.Contains(got, " 200 ") {
		t.Errorf("after a new engine the proxy refused run-vmnet-c: %s", got)
	}
	for _, chain := range []string{"INPUT", "FORWARD"} {
		rules, _ := exec.Command("iptables", "-S", chain).Output()
		if n := strings.Count(string(rules), "--mark 0x1000000/0x1000000 -j ACCEPT"); n != 1 {
			t.Errorf("%s holds the VM rule %d times:\n%s", chain, n, rules)
		}
	}
}

// relayDial reaches addr in the VM the way ssh -L does: through the
// forwarding relay the server starts in the session.
func relayDial(t *testing.T, e *engine, key, addr string) string {
	t.Helper()
	ex, err := e.startManagedExec(t.Context(), key, runtime.ExecSpec{
		Argv: []string{coordtransport.CLIPath, devexec.Command, devexec.Forward}, CreationKey: "relay-" + key, Lossless: true,
	}, false)
	if err != nil {
		return "error: " + err.Error()
	}
	att := ex.Attachment()
	defer func() { _, _ = ex.Stop(context.Background(), time.Second) }()
	go func() { _, _ = io.Copy(io.Discard, att.Stderr()) }()
	conn, chans, reqs, err := ssh.NewClientConn(devexec.Stream{Reader: att.Stdout(), Writer: att.Stdin(), Closer: att}, "", &ssh.ClientConfig{
		HostKeyCallback: func(string, net.Addr, ssh.PublicKey) error { return nil },
	})
	if err != nil {
		return "error: " + err.Error()
	}
	defer func() { _ = conn.Close() }()
	go ssh.DiscardRequests(reqs)
	go func() {
		for nc := range chans {
			_ = nc.Reject(ssh.Prohibited, "no channels")
		}
	}()
	host, port, _ := net.SplitHostPort(addr)
	var portNum uint32
	_, _ = fmt.Sscan(port, &portNum)
	ch, chReqs, err := conn.OpenChannel("direct-tcpip", ssh.Marshal(struct {
		DestHost string
		DestPort uint32
		OrigHost string
		OrigPort uint32
	}{host, portNum, "127.0.0.1", 0}))
	if err != nil {
		return "error: " + err.Error()
	}
	go ssh.DiscardRequests(chReqs)
	defer func() { _ = ch.Close() }()
	time.AfterFunc(10*time.Second, func() { _ = ch.Close() })
	line, _ := bufio.NewReader(ch).ReadString('\n')
	return "connected: " + strings.TrimSpace(line)
}
