package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/3xDevOps/Aether/internal/browser"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// BrowserFrames starts a companion stream only for a real viewer. Its single
// pending frame is replaced rather than accumulating stale UI input targets.
func (s *Scheduler) DevelopmentBrowserFrames(ctx context.Context, id domain.RunID, p control.Principal, target protocol.DevBrowserPageTarget, authorize func() error) (<-chan protocol.DevBrowserFrame, func(), error) {
	if target.RunID != "" && target.RunID != string(id) {
		return nil, nil, control.ErrInvalid
	}
	auth := func() error { return s.developmentAuthorization(ctx, id, p, authorize) }
	if err := auth(); err != nil {
		return nil, nil, err
	}
	live, err := s.ResolveLiveRun(ctx, id, false)
	if err != nil {
		return nil, nil, err
	}
	status, client, err := s.browserClient(ctx, live, false)
	if err != nil {
		return nil, nil, err
	}
	if status.SessionID != target.SessionID || target.SessionID == "" {
		return nil, nil, control.ErrStale
	}
	request := browserTarget(target)
	request.Operation = "select"
	if err := request.Validate(); err != nil {
		return nil, nil, err
	}
	streamCtx, cancelCause := context.WithCancelCause(ctx)
	cancel := func() { cancelCause(context.Canceled) }
	frames := make(chan protocol.DevBrowserFrame, 1)
	publish := func(frame protocol.DevBrowserFrame) {
		select {
		case frames <- frame:
		default:
			select {
			case <-frames:
			default:
			}
			select {
			case frames <- frame:
			default:
			}
		}
	}
	go func() {
		// A quiet/static page still loses observation access promptly. The monitor
		// cancels the private Unix stream, including a blocked frame read.
		monitorDone := make(chan struct{})
		go func() {
			defer close(monitorDone)
			tick := time.NewTicker(500 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-streamCtx.Done():
					return
				case <-tick.C:
					if err := auth(); err != nil {
						cancelCause(err)
						return
					}
					current, err := s.ResolveLiveRun(streamCtx, id, false)
					if err != nil {
						cancelCause(err)
						return
					}
					if current.ContainerID != live.ContainerID {
						cancelCause(control.ErrStale)
						return
					}
				}
			}
		}()
		err := client.Stream(streamCtx, browserTarget(target), func(c browser.Capture) error {
			if err := auth(); err != nil {
				return err
			}
			current, err := s.ResolveLiveRun(streamCtx, id, false)
			if err != nil {
				return err
			}
			if current.ContainerID != live.ContainerID {
				return control.ErrStale
			}
			m := c.Metadata
			if m.SessionID != target.SessionID || m.PageID != target.PageID {
				return errors.New("browser stream identity changed")
			}
			publish(protocol.DevBrowserFrame{Metadata: protocol.DevBrowserFrameMetadata{RunID: string(id), SessionID: m.SessionID, PageID: m.PageID, PageRevision: m.PageRevision, ViewportID: m.ViewportID, Width: m.Width, Height: m.Height, Timestamp: m.CapturedAt, MIMEType: m.ContentType, Sequence: m.Sequence, OffsetTop: m.OffsetTop, PageScaleFactor: m.PageScaleFactor, ScrollX: m.ScrollX, ScrollY: m.ScrollY}, Data: c.Bytes})
			return nil
		})
		if cause := context.Cause(streamCtx); cause != nil {
			err = cause
		}
		cancel()
		<-monitorDone
		if err != nil {
			publish(protocol.DevBrowserFrame{Err: err})
		}
		close(frames)
	}()
	return frames, cancel, nil
}
