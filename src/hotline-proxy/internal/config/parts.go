package config

// Parts is a set of the parts of a call that an api checks against its
// spec.
type Parts uint16

// The parts a call is checked in, written in the config's checks list.
const (
	PartPath Parts = 1 << iota
	PartQuery
	PartHeaders
	PartCookies
	PartBody
	PartStatus
	PartResponseHeaders
	PartResponseBody
	// PartMessages are the messages of a WebSocket session (Peter,
	// 2026-11-19, WebSocket slice 2, Q27).
	PartMessages

	// AllParts is every part, which is what leaving checks out means.
	AllParts = PartPath | PartQuery | PartHeaders | PartCookies | PartBody |
		PartStatus | PartResponseHeaders | PartResponseBody | PartMessages
	// RequestParts and ResponseParts are the parts of each side.
	RequestParts  = PartPath | PartQuery | PartHeaders | PartCookies | PartBody
	ResponseParts = PartStatus | PartResponseHeaders | PartResponseBody
)

// partNamed finds the part a checks entry names.
func partNamed(name string) (Parts, bool) {
	switch name {
	case "path":
		return PartPath, true
	case "query":
		return PartQuery, true
	case "headers":
		return PartHeaders, true
	case "cookies":
		return PartCookies, true
	case "body":
		return PartBody, true
	case "status":
		return PartStatus, true
	case "response-headers":
		return PartResponseHeaders, true
	case "response-body":
		return PartResponseBody, true
	case "messages":
		return PartMessages, true
	}
	return 0, false
}

// Has says whether every part of p2 is in p.
func (p Parts) Has(p2 Parts) bool { return p&p2 == p2 }

// Any says whether some part of p2 is in p.
func (p Parts) Any(p2 Parts) bool { return p&p2 != 0 }
