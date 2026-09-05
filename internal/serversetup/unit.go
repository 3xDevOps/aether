package serversetup

// ServiceName is the system service identifier.
const ServiceName = "aether-server"

// UnitPath is where the system unit belongs.
const UnitPath = "/etc/systemd/system/" + ServiceName + ".service"

// ActivateCommand enables and starts a freshly installed unit.
const ActivateCommand = "systemctl daemon-reload && systemctl enable --now " + ServiceName

// RestartCommand applies a changed config file to a running service.
const RestartCommand = "systemctl restart " + ServiceName

// ServiceDefaults are the options the packaged unit used to hardcode in its
// ExecStart before they moved into the config file. They are the posture of
// a machine running aether-server as a service. Install seeds a new config
// file with these so moving the options out of the unit does not quietly
// change what a fresh install does.
func ServiceDefaults() map[string]string {
	return map[string]string{
		"data-dir": "/var/lib/aether",
		"addr":     ":2222",
	}
}

// DefaultUnit returns the systemd unit for the server. It is pinned
// byte-for-byte to packaging/systemd/aether-server.service by a test, so the
// shipped file and everything installed by this binary can never drift.
func DefaultUnit() string { return defaultUnit }

const defaultUnit = `[Unit]
Description=Aether agent development server
Documentation=https://github.com/3xDevOps/Aether/blob/main/docs/install.md
Wants=network-online.target
After=network-online.target docker.service
Requires=docker.service

[Service]
Type=simple
# Runs as root. Two reasons, both deliberate (docs/security.md):
#   1. The unit needs the Docker socket, and Docker socket access is already
#      root-equivalent on the host - a dedicated user in the docker group
#      buys nothing.
#   2. Environment images whose configured user is non-root make the server
#      chown run checkouts and credential homes to that UID, which needs
#      CAP_CHOWN. A non-root server can only serve root images.
# To run unprivileged anyway: add User=aether and SupplementaryGroups=docker,
# keep every environment image on a root user, and give the user read access to
# the tailscaled socket (tailscale set --operator=aether) if you want tailnet
# identity auth.
#
# Options (listen address, data dir, tailnet policy) live in
# the config file below, which is operator-owned and survives binary updates
# and unit reinstalls. Set them with "aether-server config set <key> <value>"
# or "aether-server setup" - do not edit this ExecStart.
ExecStart=/usr/local/bin/aether-server serve --config ` + DefaultConfigPath + `

# Optional: ANTHROPIC_API_KEY / OPENAI_API_KEY for API-key harnesses, passed
# through into run containers. Subscription logins do not belong here - they
# live in the per-member server-side home that ` + "`aether setup`" + ` writes.
EnvironmentFile=-/etc/aether/aether-server.env

StateDirectory=aether
StateDirectoryMode=0700

Restart=on-failure
RestartSec=5s
# Shutdown stops the live containers before the process exits; give it room.
KillSignal=SIGTERM
TimeoutStopSec=90
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`
