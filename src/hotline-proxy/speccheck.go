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
	// dropped says the check gave way in a full line, and never ran.
	dropped bool
}

func newSpecCheck() *specCheck { return &specCheck{done: make(chan struct{})} }

// begin hands the check to the line at start, once: the first caller wins.
// When after is set, the check joins the line only once after is closed.
func (s *specCheck) begin(start time.Time, line *checkLine, body *tap, after <-chan struct{}, check func() spec.Checked) {
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
	if c.api == nil {
		return
	}
	// With no request part to check, the route is already found, unless
	// the spec names it in the body.
	if !c.api.Checks.Any(config.RequestParts) && !c.api.Spec.NamesActionInBody() {
		return
	}
	c.reqCheck.begin(start, c.h.checks, c.reqBody, nil, func() spec.Checked {
		body := c.reqBody.copied()
		if c.api.Spec.NamesActionInBody() {
			// Only this check writes the route, before it ends; the reply's
			// check and the record read it after.
			c.route = c.api.Spec.Find(spec.Exchange{
				Method:      c.in.Method,
				EscapedPath: c.target.EscapedPath(),
				RawQuery:    c.target.RawQuery,
				Header:      c.in.Header,
				Action:      c.api.Spec.BodyAction(c.in.Header.Get("Content-Type"), body),
			})
		}
		found := c.api.Spec.CheckRequest(c.route, spec.Exchange{
			Method:      c.in.Method,
			EscapedPath: c.target.EscapedPath(),
			RawQuery:    c.target.RawQuery,
			Header:      c.in.Header,
			Body:        body,
		})
		c.reqBody.drop()
		return picked(c.api.Checks, found, true)
	})
}

// checkReply starts the check of the reply.
func (c *call) checkReply(start time.Time, resp *http.Response) {
	if c.api == nil || resp == nil || !c.api.Checks.Any(config.ReplyParts) {
		return
	}
	var after <-chan struct{}
	if c.api.Spec.NamesActionInBody() {
		// The route is known only once the request's check has read the
		// body, so the reply's check joins the line after it, and never
		// holds a worker while it waits. finish starts the request check
		// for every call.
		after = c.reqCheck.done
	}
	c.replyCheck.begin(start, c.h.checks, c.respBody, after, func() spec.Checked {
		// For a spec that names its action in the body, the request's
		// check has ended and found the route; when it gave way in a full
		// line, there is no route.
		found := c.api.Spec.CheckReply(c.route, spec.Exchange{
			Status:      resp.StatusCode,
			ReplyHeader: resp.Header,
			ReplyBody:   c.respBody.copied(),
		})
		c.respBody.drop()
		return picked(c.api.Checks, found, false)
	})
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
	case "path":
		return config.PartPath
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
	return 0
}
