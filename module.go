package bgp

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"wayseer.dev/sdk"
)

// Kind is the module kind in config.
const Kind = "bgp"

const version = "1"

// eventsKept is how many recent events QueryEvents answers from.
const eventsKept = 1000

// init registers the kind for a build of the app that imports the package.
func init() { sdk.Register(Kind, func() sdk.Module { return New() }) }

// Module reads BGP updates from RIS Live and sends what changed every interval.
type Module struct {
	health atomic.Pointer[sdk.Health]

	mu      sync.Mutex // guards what follows, shared by Run and the queries
	name    sdk.ModuleID
	opts    options
	watch   watch
	subs    []subscription
	state   *state
	tracker sdk.Tracker
	sent    map[sdk.EntityRef]sdk.Entity // as last sent
	events  *sdk.EventLog
}

// New makes an unconfigured module.
func New() *Module { return &Module{} }

// Info describes the module.
func (m *Module) Info() sdk.Info {
	return sdk.Info{Kind: Kind, Version: version, Description: "Live BGP routing for watched prefixes and ASes, from RIPE RIS Live"}
}

// Configure checks the options; nothing is read until Run.
func (m *Module) Configure(_ context.Context, cfg sdk.Config) error {
	o := defaults()
	if err := cfg.Decode(&o); err != nil {
		return err
	}
	w, err := o.validate()
	if err != nil {
		return fmt.Errorf("line %d: %w", cfg.Line, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.name, m.opts, m.watch, m.subs = cfg.Name, o, w, subscriptions(&o)
	m.state, m.sent, m.events = newState(cfg.Name, &o, w), nil, sdk.NewEventLog(eventsKept)
	m.health.Store(&sdk.Health{})
	return nil
}

// Run sends a snapshot, then a delta every interval, until ctx ends.
func (m *Module) Run(ctx context.Context, sink sdk.Sink) error {
	m.mu.Lock()
	m.tracker.Reset()
	every := m.opts.Interval
	m.mu.Unlock()
	send := sink.Snapshot
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		if err := send(ctx, m.tick(time.Now())); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		send = sink.Delta
		t.Reset(every)
	}
}

// tick closes the interval and returns what changed since the last send, with its events.
func (m *Module) tick(now time.Time) *sdk.ChangeSet {
	m.mu.Lock()
	defer m.mu.Unlock()
	evs := m.state.flush(now)
	ents, edges := m.state.world()
	m.sent = ents
	cs := m.tracker.Changes(ents, edges, now)
	cs.Events = evs
	m.events.Add(evs...)
	return cs
}

// Discover returns the working set as it is now.
func (m *Module) Discover(ctx context.Context) (*sdk.ChangeSet, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var fresh sdk.Tracker
	ents, edges := m.state.world()
	return fresh.Changes(ents, edges, time.Now()), nil
}

// QueryEvents answers from the events sent since the module started.
func (m *Module) QueryEvents(ctx context.Context, q sdk.EventQuery) ([]sdk.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.events.Query(q), nil
}

// Health reports whether RIS Live is being read.
func (m *Module) Health() sdk.Health {
	if h := m.health.Load(); h != nil {
		return *h
	}
	return sdk.Health{}
}
