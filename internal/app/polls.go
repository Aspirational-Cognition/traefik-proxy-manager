package app

import (
	"sync"
	"time"
)

// pollLog records Traefik's fetches of GET /traefik/config: when it first
// fetched each store version, and when it last fetched any.
type pollLog struct {
	mu    sync.Mutex
	polls []poll
	last  time.Time
}

type poll struct{ at, version time.Time }

// record notes that Traefik fetched store version v at time at.
func (l *pollLog) record(at, v time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.last = at
	if n := len(l.polls); n == 0 || !l.polls[n-1].version.Equal(v) {
		l.polls = append(l.polls, poll{at, v})
	}
}

// servedAt is when Traefik first fetched a config holding version v of a
// Proxy Host, if it has.
func (l *pollLog) servedAt(v time.Time) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range l.polls {
		if !p.version.Before(v) {
			return p.at, true
		}
	}
	return time.Time{}, false
}

// lastAt is when Traefik last fetched the config, if it has since tpm started.
func (l *pollLog) lastAt() (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last, !l.last.IsZero()
}
