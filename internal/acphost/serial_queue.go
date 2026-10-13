package acphost

import "sync"

// serialQueue runs callbacks one at a time in push order on its own
// goroutine, so callbacks may call back into the Session.
type serialQueue struct {
	mu     sync.Mutex
	fns    []func()
	closed bool
	done   chan struct{}
	wake   chan struct{}
}

func newSerialQueue() *serialQueue { return &serialQueue{wake: make(chan struct{}, 1)} }

func (q *serialQueue) push(f func()) {
	q.mu.Lock()
	q.fns = append(q.fns, f)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// close stops the queue after the callbacks already pushed and then closes
// done, if given.
func (q *serialQueue) close(done chan struct{}) {
	q.mu.Lock()
	q.closed, q.done = true, done
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *serialQueue) run() {
	for {
		q.mu.Lock()
		fns, closed, done := q.fns, q.closed, q.done
		q.fns = nil
		q.mu.Unlock()
		for _, f := range fns {
			f()
		}
		if len(fns) > 0 {
			continue
		}
		if closed {
			if done != nil {
				close(done)
			}
			return
		}
		<-q.wake
	}
}
