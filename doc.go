// Package bgp shows live BGP routing from RIPE RIS Live in Wayseer: the prefixes and ASes the
// owner watches, the peers and collectors that see them, and how their routes change.
//
// It subscribes to RIS Live with server-side filters (prefixes, origin ASNs, collectors, peers,
// message types) and is never sent the unfiltered stream. It is a lens: it keeps only a bounded,
// ageing working set in memory. See README.md for options and the data it sends.
//
// RIS data is a service of the RIPE NCC (https://www.ripe.net/ris); use it under the RIPE NCC's
// terms, which ask that the RIPE NCC and RIS be credited as its source.
package bgp
