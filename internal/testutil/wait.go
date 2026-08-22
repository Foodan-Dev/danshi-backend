package testutil

import "context"

type callSignal struct {
	changed chan struct{}
}

func newCallSignal() callSignal {
	return callSignal{changed: make(chan struct{})}
}

func (s *callSignal) notify() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func waitForCalls(
	ctx context.Context,
	current func() (int, <-chan struct{}),
	want int,
) bool {
	for {
		count, changed := current()
		if count >= want {
			return true
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return false
		}
	}
}

func waitForRelease(ctx context.Context, release <-chan struct{}) error {
	if release == nil {
		return nil
	}
	select {
	case <-release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
