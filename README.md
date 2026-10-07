# Wayseer module: BGP (RIS Live)

An external module for Wayseer Desktop that shows live BGP routing from
[RIPE RIS Live](https://ris-live.ripe.net/). You name the prefixes and origin ASes to watch.
The module then shows who announces them, through which AS paths, which peers and collectors
see them, and how their routes change. It raises events for possible hijacks, origin changes,
new more-specifics, withdrawals and path changes.

It is not built into the app. It runs as its own program from a signed package, as
`wayseer-labs/bgp` in the namespace `bgp`. Like every Wayseer module it is a lens, not a
store. It keeps only a bounded working set in memory and forgets what it no longer sees.

## Configuration

```yaml
modules:
  - kind: external
    name: bgp
    options:
      module: wayseer-labs/bgp
      options:
        prefixes: [193.0.0.0/21, 2001:67c:2e8::/48]
        expect: {193.0.0.0/21: [3333], 2001:67c:2e8::/48: [3333]}
        hosts: [rrc00, rrc21]
```

| Option | Default | Meaning |
|---|---|---|
| `url` | `wss://ris-live.ripe.net/v1/ws/` | RIS Live. Use `wss://` or `ws://` for WebSocket, or `https://ris-live.ripe.net/v1/stream/?format=json` for its HTTP stream. |
| `client` | `wayseer-bgp` | The client identifier sent to RIS Live as `?client=`, as RIPE asks. Name your organisation. |
| `prefixes` | | Prefixes to watch, at most 100. They must be at least /8 (IPv4) or /16 (IPv6) while `more_specific` is on. |
| `more_specific` | `true` | Also watch prefixes inside them. |
| `less_specific` | `false` | Also watch prefixes that contain them. |
| `origins` | | ASNs whose every announcement is watched, at most 32. |
| `expect` | | The legitimate origin ASNs of a watched prefix. Any other origin of it, or of a more-specific, is a possible hijack. |
| `hosts` | all | Collectors to read, such as `rrc00`. |
| `peers` | all | Peer addresses to read. |
| `types` | `[UPDATE, RIS_PEER_STATE]` | The message types to read. |
| `interval` | `5s` | How often changes and series points are sent, from 1s to 1m. |
| `stale_after` | `1h` | How long something no route uses is kept unseen. |
| `max_ases` | `2000` | The most ASes kept. The least recently seen go first. |
| `max_links` | `5000` | The most AS adjacencies kept. |
| `max_prefixes` | `200` | The most prefixes kept. Watched prefixes are always kept. |

At least one of `prefixes` or `origins` is required, and `types` must include `UPDATE`. Each
host and peer you name multiplies the subscriptions, and a config that needs more than 64 is
refused. RIS Live is never asked for its unfiltered stream.

## What it shows

| Kind | Native ID | Is |
|---|---|---|
| `bgp/prefix` | `193.0.0.0/21` | A watched prefix, a more- or less-specific of one, or a prefix a watched origin announced. |
| `bgp/as` | `AS3333` | An AS seen in the path of a watched route, or as a peer's AS. |
| `bgp/peer` | `192.0.2.1@rrc00` | A RIS peer, at its collector. |
| `bgp/collector` | `rrc00` | A RIS route collector. |

**Edges.** An AS `talks_to` the next AS toward the origin in a path, so the arrows follow
traffic toward the prefix. The origin AS `owns` the prefix it announces, and a peer's AS
`owns` the peer. A peer is a `member_of` its collector. A watched prefix is the `parent_of`
its more-specifics, and a less-specific is the `parent_of` the watched prefix.

**Status of a prefix.** The status is `crit` when an origin isn't one `expect` names. It is
`down` once every peer seen carrying the prefix has withdrawn it, and `warn` when more than one
AS originates it. It is `unknown` until an announcement is seen. RIS Live streams only changes,
so the module starts empty and learns routes as peers announce them. A route that is never
re-announced is still kept until it is withdrawn.

**Series**, one point per interval:

| Metric | Unit | Kinds |
|---|---|---|
| `bgp.announcements` | per second | prefix, peer, collector |
| `bgp.withdrawals` | per second | prefix, peer, collector |
| `bgp.path_changes` | per second | prefix |
| `bgp.peers` | count | prefix (peers seen carrying it) |

**Events:**

| Kind | Severity | When |
|---|---|---|
| `origin` | critical | Possible hijack: an origin not in `expect` announces the prefix or a more-specific. |
| `origin` | warn | Without `expect`, the prefix gets an origin it didn't have. |
| `more-specific` | warn | A more-specific of a watched prefix is announced for the first time. |
| `withdrawal` | error / info | A watched prefix was withdrawn: error when no peer seen carries it any more, info otherwise. There is at most one per interval. |
| `path-change` | info | A peer's AS path to a watched prefix changed. There is at most one per interval. |
| `peer-state` | warn / info | A known peer's BGP session went down, which drops its routes, or came back up. |

**Bounds.** Watched prefixes are always kept. Other prefixes, ASes, adjacencies, peers (1000)
and collectors (64) are capped, and the least recently seen go first. Whatever no current route
uses is forgotten after `stale_after`. At most 500 events are kept between sends, and the rest
are counted in one `dropped` event. Each series keeps 360 points.

**Connection.** The module reconnects after a dropped stream. It waits 1s, then twice as long
each time, up to a minute, and starts again from 1s once a stream has lasted a minute. A
`ris_error` from RIS Live shows as the module's error, and a message it can't read shows as a
note. The WebSocket connects directly. The HTTP stream honours `HTTPS_PROXY`.

## Data source and attribution

The data comes from the RIPE NCC's Routing Information Service (RIS), at
<https://www.ripe.net/ris>, through RIS Live. Its use falls under the RIPE NCC's
[terms of service](https://www.ripe.net/about-us/legal/) for the website and its publicly
available services. Commercial use is allowed under the RIPE NCC's
[RIS commercial use rules](https://www.ripe.net/analyse/internet-measurements/routing-information-service-ris/commercial-use),
which ask for the RIPE NCC logo, a link to <https://www.ripe.net/ris>, and this description:

> The RIPE Routing Information Service (RIS) is a RIPE NCC service. With the help of network
> operators all over the world, RIS employs a globally distributed set of Remote Route
> Collectors (RRCs), typically located at Internet Exchange Points, to collect and store
> Internet routing data. Volunteers peer with the RRCs using the BGP protocol and RIS stores
> the update and withdraw messages. RIS data can be accessed via: - RIPEstat, the "one-stop
> shop" for all available information about Internet number resources. RIPEstat uses
> individual widgets to display routing and other information; - RIS Live, a real time BGP
> streaming API allowing server-side filtering of BGP messages by prefix or autonomous system;
> - RIS Raw Data, available for each route collector, with state dumps and batches of updates
> made available periodically; - RISwhois, that searches the latest RIS data for details of an
> IP address using a plaintext "whois"-style interface. It is useful when querying RIS data
> using scripts.

RIS Live needs no account. Set `client` to name yourself, and keep your filters narrow. RIPE
closes connections that can't keep up.

## Working on it

```
make check   # what CI runs: tests with the conformance suite, vet and lint for every platform, a key scan
make help    # every target
```

Tests never reach the network. `fake_test.go` is a fake RIS Live that serves
`testdata/stream.ndjson` over WebSocket, and the same file is served as a static file to stand
in for the HTTP stream. The fixture is made up. It follows RIS Live's message format, but uses
documentation addresses and ASNs (RFC 5737, RFC 3849, RFC 5398) and holds no RIS data.
`testdata/world.txt` and `events.txt` are what the fixture builds. Run the tests with
`UPDATE_SNAPSHOTS=1` to write them again, then read the diff.

The module imports only the SDK (`wayseer.dev/sdk`), the standard library and
`golang.org/x/net/websocket`, which the SDK already brings in. `TestImportsOnlyTheSDK` keeps it
free of the app. golangci-lint is pinned in `tools/go.mod`, and gitleaks runs at a pinned
version through `go run`. `TestMakeSignPackagesTheModule` signs a package with throwaway keys
when Wayseer's source is in `../../core` or `../core`, and skips otherwise.

To sign a package for yourself, see Wayseer's guide, "Writing a module": `make sign` with
`WAYSEER_DEV_KEY` and `WAYSEER_DEV_CERT` set.

For a marketplace submission, the conformance options read the fixture as the sandbox serves
it:

```yaml
conformance:
  options: |
    url: http://127.0.0.1:8080/stream.ndjson
    interval: 1s
    prefixes: [203.0.113.0/24, 2001:db8::/32]
    origins: [65551]
    expect: {203.0.113.0/24: [64500], 2001:db8::/32: [65550]}
  failing: |
    url: http://127.0.0.1:1/
    prefixes: [203.0.113.0/24]
  fixture: testdata
```

## Licence

MIT; see `LICENSE`. The RIS data the module reads is the RIPE NCC's, under its own terms (above).
