package protocol

const (
	// MethodRunSudo runs a command as root in a Default run's container once
	// a person allows it. The sudo shim calls it on the coordination socket,
	// one request per connection.
	MethodRunSudo = "run.sudo"
	// MethodRunSudoOutput notifications carry the command's output ahead of
	// the run.sudo response.
	MethodRunSudoOutput = "run.sudo.output"
	// RunSudoMaxStdin bounds the standard input the shim forwards.
	RunSudoMaxStdin = 1 << 20
)

// RunSudoParams is one sudo command. User empty is root. StdinBytes raw
// bytes of the command's standard input follow the request line.
type RunSudoParams struct {
	Argv           []string `json:"argv"`
	User           string   `json:"user,omitempty"`
	Dir            string   `json:"dir"`
	StdinBytes     int      `json:"stdin_bytes,omitempty"`
	NonInteractive bool     `json:"non_interactive,omitempty"`
}

// RunSudoOutput is one chunk of the command's stdout, or stderr with Stderr.
type RunSudoOutput struct {
	Stderr bool   `json:"stderr,omitempty"`
	Data   []byte `json:"data"`
}

type RunSudoResult struct {
	ExitCode int `json:"exit_code"`
}
