package runtime

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// ExecOutputLimit covers stdout and stderr together.
const ExecOutputLimit = 1 << 20

// setupOutputLimit is how much of a setup script's output is kept, from the
// end, where the failure is.
const setupOutputLimit = 16 << 10

// RootPath is sudo's secure_path. A root exec never searches the
// container's PATH, which puts the agent-writable ~/.local/bin first.
const RootPath = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// UserScript gives uid $1 a passwd entry whose home is $2: git refuses to
// commit and ssh and ssh-keygen refuse to run for a uid with none, and ssh
// reads ~/.ssh from the passwd home, never $HOME. With $3 set it also lets
// the uid sudo without a password; the sudoers file is checked before it
// goes live, since sudo refuses to run at all with a broken one.
//
// The home at /root is the member's, shared by every container they run,
// and systemd's postinst runs systemd-tmpfiles, whose provision.conf hands
// /root to root: installing any package that pulls in systemd would take
// the home from all of them. The /etc entry masks it.
const UserScript = `uid=${1%%:*} gid=${1#*:}
if grep -q "^[^:]*:[^:]*:$uid:" /etc/passwd; then
	awk -F: -v OFS=: -v u="$uid" -v h="$2" '$3 == u { $6 = h } { print }' /etc/passwd > /etc/passwd.aether && cat /etc/passwd.aether > /etc/passwd && rm -f /etc/passwd.aether || exit 1
else
	echo "aether:x:$uid:$gid::$2:/bin/sh" >> /etc/passwd || exit 1
fi
grep -q "^[^:]*:[^:]*:$gid:" /etc/group || echo "aether:x:$gid:" >> /etc/group || exit 1
mkdir -p /etc/tmpfiles.d && ln -sfn /dev/null /etc/tmpfiles.d/provision.conf || exit 1
[ -n "$3" ] || exit 0
mkdir -p /etc/sudoers.d
printf '#%s ALL=(ALL:ALL) NOPASSWD: ALL\n' "$uid" > /etc/sudoers.d/.aether.new
chmod 0440 /etc/sudoers.d/.aether.new
if command -v visudo >/dev/null 2>&1 && ! visudo -cqf /etc/sudoers.d/.aether.new; then rm -f /etc/sudoers.d/.aether.new; exit 1; fi
mv -f /etc/sudoers.d/.aether.new /etc/sudoers.d/aether`

func Unprivileged(user string) bool {
	uid, _, _ := strings.Cut(user, ":")
	return uid != "" && uid != "0"
}

func EnvList(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		out = append(out, k+"="+env[k])
	}
	return out
}

func EnvValue(env []string, name string) string {
	for _, pair := range env {
		if key, value, _ := strings.Cut(pair, "="); key == name {
			return value
		}
	}
	return ""
}

func TrustCheckouts(env map[string]string, checkout string, extras []Mount) map[string]string {
	env = trustCheckout(env, checkout)
	for _, m := range extras {
		env = trustCheckout(env, m.ContainerPath)
	}
	return env
}

// Execs inherit the container env, so this also covers every git the server
// runs inside the container.
func trustCheckout(env map[string]string, checkout string) map[string]string {
	if checkout == "" {
		return env
	}
	n := 0
	if count, ok := env["GIT_CONFIG_COUNT"]; ok {
		parsed, err := strconv.Atoi(count)
		if err != nil || parsed < 0 {
			return env
		}
		n = parsed
	}
	out := make(map[string]string, len(env)+3)
	maps.Copy(out, env)
	out["GIT_CONFIG_KEY_"+strconv.Itoa(n)] = "safe.directory"
	out["GIT_CONFIG_VALUE_"+strconv.Itoa(n)] = checkout
	out["GIT_CONFIG_COUNT"] = strconv.Itoa(n + 1)
	return out
}

func secretEnvValues(label string, env []string) []string {
	var names []string
	if json.Unmarshal([]byte(label), &names) != nil {
		return nil
	}
	return SecretValues(names, env)
}

func SecretValues(names, env []string) []string {
	var values []string
	for _, pair := range env {
		name, value, _ := strings.Cut(pair, "=")
		if value != "" && slices.Contains(names, name) {
			values = append(values, value)
		}
	}
	return values
}

const secretMask = "***"

// SetupOutput masks exact secret values, longest first so a secret holding
// another is masked whole, then keeps the last setupOutputLimit bytes. cut
// says output already lost its beginning, which may have split a secret: the
// span that could hold its remainder is dropped. That span is measured after
// masking, where a shorter secret inside the remainder can have grown it to
// len(secretMask) times its length.
func SetupOutput(output string, cut bool, secrets []string) string {
	if len(secrets) > 0 {
		slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
		pairs := make([]string, 0, 2*len(secrets))
		for _, secret := range secrets {
			pairs = append(pairs, secret, secretMask)
		}
		output = strings.NewReplacer(pairs...).Replace(output)
		if cut {
			output = output[min(len(secretMask)*len(secrets[0]), len(output)):]
		}
	}
	if cut || len(output) > setupOutputLimit {
		output = output[max(0, len(output)-setupOutputLimit):]
		if i := strings.IndexByte(output, '\n'); i >= 0 {
			output = output[i+1:]
		}
		output = "[earlier output omitted]\n" + output
	}
	return strings.ToValidUTF8(output, "�")
}
