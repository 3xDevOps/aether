//go:build kvm

package vm

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/runtime"
)

// homeReads times, with nothing cached in the guest, reading a 256 MiB
// file, 2,000 small files and starting a program from the home, at the
// kernel's read-ahead and then at the one the helper set, and the peak
// memory of the helper, which serves the reads.
const homeReads = `set -e
bdi=/sys/class/bdi/$(mountpoint -d /root)/read_ahead_kb
helper=$(cat $bdi)
echo "read-ahead $helper KiB"
busy() { set -- $(head -1 /proc/stat); echo $(($2 + $3 + $4 + $7 + $8)); }
ms() { echo $((($(date +%s%N) - $1) / 1000000)); }
cold() { sync; echo 3 > /proc/sys/vm/drop_caches; }
for ra in 128 $helper; do
	echo $ra > $bdi
	echo 5 > /proc/1/clear_refs
	cold; t=$(date +%s%N); c=$(busy)
	cat /root/big > /dev/null
	big=$(ms $t); cpu=$((($(busy) - c) * 10))
	cold; t=$(date +%s%N)
	cat /root/tree/* > /dev/null
	tree=$(ms $t)
	cold; t=$(date +%s%N)
	/opt/aether-vmtest -test.run '^$' > /dev/null
	start=$(ms $t); t=$(date +%s%N)
	/opt/aether-vmtest -test.run '^$' > /dev/null
	echo "read-ahead ${ra} KiB: 256 MiB in ${big} ms using ${cpu} ms of guest CPU, 2000 files in ${tree} ms, program start ${start} ms cold and $(ms $t) ms warm, helper peak $(sed -n 's/^VmHWM:[^0-9]*//p' /proc/1/status)"
done
`

// TestKVMHomeReadsFast times reads from the home in a session with a
// one-CPU limit, checks that the helper sets the home's read-ahead and that
// the reads log nothing to the serial console, and that a write from
// another VM shows in a file the guest has cached at the next open. The
// read-ahead gain, 5-30% on hosted runners, is too noisy to assert.
func TestKVMHomeReadsFast(t *testing.T) {
	e := kvmEngine(t)
	src := filepath.Join(e.root, "..", "src-homeread")
	t.Cleanup(func() { _ = os.RemoveAll(src) })
	files := map[string]string{"big": strings.Repeat("x", 256<<20)}
	for i := range 2000 {
		files[fmt.Sprintf("tree/%04d.js", i)] = strings.Repeat("y", 8<<10)
	}
	writeHome(t, filepath.Join(src, "home"), files)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	mounts := []runtime.Mount{
		{HostPath: filepath.Join(src, "home"), ContainerPath: "/root"},
		{HostPath: self, ContainerPath: "/opt/aether-vmtest", ReadOnly: true},
	}
	kvmSession(t, e, "run-vmread-a", mounts...)
	kvmSession(t, e, "run-vmread-b", mounts...)
	out, code := vmRun(t, e, "run-vmread-a", homeReads)
	if code != 0 {
		t.Fatalf("reads: %d %s", code, out)
	}
	t.Logf("cold reads from the home, one vCPU:\n%s", out)
	if !strings.HasPrefix(out, "read-ahead 512 KiB\n") {
		t.Error("the helper did not set the home's read-ahead to 512 KiB")
	}
	if console, _ := os.ReadFile(e.path("run-vmread-a", consoleLog)); strings.Contains(string(console), "trySplice") {
		t.Error("reads from the home logged go-fuse's trySplice failures to the serial console")
	}

	// The guest keeps the pages of a file the host has not changed across
	// opens, and drops them at the open after another VM wrote it.
	at := strconv.Itoa(128 << 20)
	cached := "sed -n 's/^Cached: *//p' /proc/meminfo"
	out, code = vmRun(t, e, "run-vmread-a", "cat /root/big > /dev/null && "+cached+" && dd if=/root/big bs=1 skip="+at+" count=7 2>/dev/null && echo && "+cached)
	var before, after int
	var old string
	if _, err := fmt.Sscanf(out, "%d kB\n%s\n%d kB", &before, &old, &after); code != 0 || err != nil || old != "xxxxxxx" || after < before-64<<10 {
		t.Fatalf("a cached file opened again: %d %q, %v", code, out, err)
	}
	if out, code := vmRun(t, e, "run-vmread-b", "printf changed | dd of=/root/big bs=1 seek="+at+" conv=notrunc 2>&1"); code != 0 {
		t.Fatalf("write into the file from another VM: %d %s", code, out)
	}
	if out, _ := vmRun(t, e, "run-vmread-a", "dd if=/root/big bs=1 skip="+at+" count=7 2>/dev/null"); out != "changed" {
		t.Errorf("a cached file another VM wrote reads %q at the next open, want %q", out, "changed")
	}
}
