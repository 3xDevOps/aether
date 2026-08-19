package server

import (
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/sshd"
)

// Compile-time assertions that the Wave 1 providers satisfy the
// contract-pinned consumer seam interfaces (wave-1-contracts §8).
var (
	_ scheduler.GitEngine = (*gitengine.Engine)(nil)
	_ scheduler.PTYHost   = (*ptyhost.Host)(nil)
	_ sshd.GitTransport   = (*gitengine.Engine)(nil)
	_ sshd.PTYAttacher    = (*ptyhost.Host)(nil)
	_ sshd.RunController  = (*scheduler.Scheduler)(nil)

	// The wired server hands both consumers the lazy-init wrapper.
	_ scheduler.GitEngine = lazyGit{}
	_ sshd.GitTransport   = lazyGit{}
)
