package proxy

import (
	"net/http"
	"sync/atomic"
	"time"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/spec"
)

// specCheck is one side of a call's check against the spec, run alongside
// the call as soon as that side is whole, so it is never in the call's way
// (Peter, 2026-10-31). It waits in the check line for a free worker.
type specCheck struct {
	started atomic.Bool
	done    chan struct{}
	start   time.Time
	// waited is when a worker took the check, or when it was dropped.
	waited time.Time
	end    time.Time
	found  spec.Checked
	// route is the route the check used. It is set, like found, before
	// done closes, and read only after.
	route spec.Route
	// dropped says the check gave way in a full line, and never ran.
	dropped bool
}

func newSpecCheck() *specCheck { return &specCheck{done: make(chan struct{})} }

// begin hands the check to the line at start, once: the first caller wins.
// When after is set, the check joins the line only once after is closed.
func (s *specCheck) begin(start time.Time, line *checkLine, body *tap, after <-chan struct{}, check func() (spec.Checked, spec.Route)) {
	if !s.started.CompareAndSwap(false, true) {
		return
	}
	s.start = start
	job := &checkJob{check: s, run: check, release: body.drop, bytes: body.held()}
	if after == nil {
		line.add(job)
		return
	}
	job.late = true
	line.expect()
	go func() {
		<-after
		line.add(job)
	}()
}

// wait waits for the check to end, and says whether it ran at all.
func (s *specCheck) wait() bool {
	if !s.started.Load() {
		return false
	}
	<-s.done
	return true
}

// checkRequest starts the check of the request.
func (c *call) checkRequest(start time.Time) {
	if !c.plan.request {
		return
	}
	c.reqCheck.begin(start, c.h.checks, c.reqBody, nil, func() (spec.Checked, spec.Route) {
		body := c.reqBody.copied()
		route := c.route
		if c.plan.routeInBody {
			// The route is in the body, so this check finds it, and hands
			// it back with what it found.
			route = c.api.Spec.Find(spec.Exchange{
				Method:      c.in.Method,
				EscapedPath: c.target.EscapedPath(),
				RawQuery:    c.target.RawQuery,
				Header:      c.in.Header,
				Action:      c.api.Spec.BodyAction(c.in.Header.Get("Content-Type"), body),
			})
		}
		found := c.api.Spec.CheckRequest(route, spec.Exchange{
			Method:      c.in.Method,
			EscapedPath: c.target.EscapedPath(),
			RawQuery:    c.target.RawQuery,
			Header:      c.in.Header,
			Body:        body,
		})
		c.reqBody.drop()
		return picked(c.api.Checks, found, true), route
	})
}

// checkReply starts the check of the reply.
func (c *call) checkReply(start time.Time, resp *http.Response) {
	if resp == nil || !c.plan.reply {
		return
	}
	// The status and headers are read now, in the goroutine that has the
	// reply: once it has passed on, such as for a WebSocket's 101, the
	// forwarder may change the reply while the check runs on its own.
	status, header := resp.StatusCode, resp.Header
	var after <-chan struct{}
	if c.plan.routeInBody {
		// The route is known only once the request's check has read the
		// body, so the reply's check joins the line after it, and never
		// holds a worker while it waits. finish starts the request check
		// for every call.
		after = c.reqCheck.done
	}
	c.replyCheck.begin(start, c.h.checks, c.respBody, after, func() (spec.Checked, spec.Route) {
		// For a spec that names its action in the body, the request's
		// check has ended and found the route; when it gave way in a full
		// line, there is no route.
		route := c.specRoute()
		found := c.api.Spec.CheckReply(route, spec.Exchange{
			Status:      status,
			ReplyHeader: header,
			ReplyBody:   c.respBody.copied(),
		})
		c.respBody.drop()
		return picked(c.api.Checks, found, false), route
	})
}

// specRoute is the call's route in the spec: the one found as the call
// started, or, for a spec that names its action in the body, the one the
// request's check found. Only the reply's check and the record call it,
// once the request's check has ended; a check that gave way in a full
// line found no route.
func (c *call) specRoute() spec.Route {
	if c.plan.routeInBody {
		return c.reqCheck.route
	}
	return c.route
}

// picked keeps what the check found in the parts the api checks. A part
// left out leaves no result, and no skipped reason when it was the body
// that would have been skipped.
func picked(parts config.Parts, found spec.Checked, request bool) spec.Checked {
	if parts == config.AllParts {
		return found
	}
	results, body := &found.Reply, config.PartReplyBody
	if request {
		results, body = &found.Request, config.PartBody
	}
	kept := (*results)[:0]
	for _, r := range *results {
		if parts.Has(partOf(r.Part, request)) {
			kept = append(kept, r)
		}
	}
	*results = kept
	if !parts.Has(body) && found.Skipped != "no_spec_route" {
		found.Skipped = ""
	}
	return found
}

// partOf is the checks entry that a result's part belongs to.
func partOf(part string, request bool) config.Parts {
	switch part {
	case "query":
		return config.PartQuery
	case "cookie":
		return config.PartCookies
	case "header":
		if request {
			return config.PartHeaders
		}
		return config.PartReplyHeaders
	case "body":
		if request {
			return config.PartBody
		}
		return config.PartReplyBody
	case "status":
		return config.PartStatus
	}
	return config.PartPath
}
