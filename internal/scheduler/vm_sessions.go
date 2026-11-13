package scheduler

// vmSessionRuntime runs each session in its own VM (runtime/vm), whose file
// service serves every host file it mounts as the session user's.
type vmSessionRuntime interface{ SessionVMs() }

func (s *Scheduler) sessionVMs() bool {
	_, ok := s.cfg.Runtime.(vmSessionRuntime)
	return ok
}
