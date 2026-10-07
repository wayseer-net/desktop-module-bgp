package bgp

import (
	"context"
	"maps"
	"slices"
	"time"

	"wayseer.dev/sdk"
)

// historyPoints is how many sends each series keeps: half an hour at the default interval.
const historyPoints = 360

// Metric names.
const (
	metricAnnouncements = "bgp.announcements"
	metricWithdrawals   = "bgp.withdrawals"
	metricPathChanges   = "bgp.path_changes"
	metricPeers         = "bgp.peers"
)

var catalogue = []sdk.Metric{
	{
		Name: metricAnnouncements, Unit: sdk.UnitPerSec, Kinds: []sdk.Kind{kindPrefix, kindPeer, kindCollector},
		Description: "announcements of watched prefixes", Native: "RIS Live UPDATE announcements",
	},
	{
		Name: metricWithdrawals, Unit: sdk.UnitPerSec, Kinds: []sdk.Kind{kindPrefix, kindPeer, kindCollector},
		Description: "withdrawals of watched prefixes", Native: "RIS Live UPDATE withdrawals",
	},
	{
		Name: metricPathChanges, Unit: sdk.UnitPerSec, Kinds: []sdk.Kind{kindPrefix},
		Description: "announcements that changed a peer's AS path", Native: "RIS Live UPDATE path differing from the peer's last",
	},
	{
		Name: metricPeers, Unit: sdk.UnitCount, Kinds: []sdk.Kind{kindPrefix},
		Description: "peers seen carrying the prefix", Native: "peers whose last update announced it",
	},
}

func inCatalogue(name string) (sdk.Metric, bool) {
	i := slices.IndexFunc(catalogue, func(m sdk.Metric) bool { return m.Name == name })
	if i < 0 {
		return sdk.Metric{}, false
	}
	return catalogue[i], true
}

// Metrics lists what QuerySeries can answer.
func (m *Module) Metrics() []sdk.Metric { return slices.Clone(catalogue) }

// record adds a point per series for the interval just ended, resets the counts, and forgets
// series whose entity has gone.
func (s *state) record(now time.Time) {
	secs := now.Sub(s.flushed).Seconds()
	if s.flushed.IsZero() || secs <= 0 {
		secs = s.opts.Interval.Seconds()
	}
	s.flushed = now
	live := map[sdk.EntityRef]bool{}
	rate := func(ref sdk.EntityRef, c *counts, metrics ...string) {
		live[ref] = true
		for _, name := range metrics {
			s.add(ref, name, now, c.value(name)/secs)
		}
		*c = counts{}
	}
	for p, ps := range s.prefixes {
		ref := s.ref(kindPrefix, p.String())
		s.add(ref, metricPeers, now, float64(len(ps.routes)))
		rate(ref, &ps.counts, metricAnnouncements, metricWithdrawals, metricPathChanges)
	}
	for key, p := range s.peers {
		rate(s.ref(kindPeer, string(key)), &p.counts, metricAnnouncements, metricWithdrawals)
	}
	for name, c := range s.collectors {
		rate(s.ref(kindCollector, name), &c.counts, metricAnnouncements, metricWithdrawals)
	}
	maps.DeleteFunc(s.series, func(ref sdk.SeriesRef, _ *sdk.Ring) bool { return !live[ref.Entity] })
}

func (c *counts) value(metric string) float64 {
	switch metric {
	case metricAnnouncements:
		return float64(c.ann)
	case metricWithdrawals:
		return float64(c.wd)
	}
	return float64(c.changes)
}

func (s *state) add(ref sdk.EntityRef, metric string, now time.Time, v float64) {
	key := sdk.SeriesRef{Entity: ref, Metric: metric}
	r := s.series[key]
	if r == nil {
		r = sdk.NewRing(historyPoints)
		s.series[key] = r
	}
	r.Add(sdk.Point{T: now.UnixNano(), V: v})
}

// QuerySeries answers from the points recorded since the module started.
func (m *Module) QuerySeries(ctx context.Context, q sdk.SeriesQuery) ([]sdk.Series, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == nil {
		return nil, nil
	}
	var out []sdk.Series
	for _, ref := range m.queried(q) {
		for _, name := range q.Metrics {
			key := sdk.SeriesRef{Entity: ref, Metric: name}
			mt, ok := inCatalogue(name)
			if r := m.state.series[key]; ok && r != nil {
				out = append(out, sdk.Series{Ref: key, Unit: mt.Unit, Points: r.In(q.Window)})
			}
		}
	}
	return out, nil
}

// queried is the entities q names, or else those its filter matches, in ref order.
func (m *Module) queried(q sdk.SeriesQuery) []sdk.EntityRef {
	if len(q.Entities) > 0 {
		return q.Entities
	}
	var out []sdk.EntityRef
	for _, ref := range slices.Sorted(maps.Keys(m.sent)) {
		e := m.sent[ref]
		if q.Filter.Match(&e) {
			out = append(out, ref)
		}
	}
	return out
}
