package proxy

import (
	"net/http"
	"sync/atomic"
	"time"

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
	// action is what the request body names as the action, for a spec that
	// picks routes by a body field; only the request's check sets it. Read
	// it only once the check has ended (after done), so the request check,
	// the reply check and the record never share a field while it changes.
	action string
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
	c.reqCheck.begin(start, c.h.checks, c.reqBody, nil, func() spec.Checked {
		body := c.reqBody.copied()
		action := c.api.Spec.BodyAction(c.in.Header.Get("Content-Type"), body)
		c.reqCheck.action = action
		found := c.api.Spec.CheckRequest(spec.Exchange{
			Method:      c.in.Method,
			EscapedPath: c.target.EscapedPath(),
			RawQuery:    c.target.RawQuery,
			Header:      c.in.Header,
			Body:        body,
			Action:      action,
		})
		c.reqBody.drop()
		return found
	})
}

// checkReply starts the check of the reply.
func (c *call) checkReply(start time.Time, resp *http.Response) {
	if c.api == nil || resp == nil {
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
		// Only a spec that names its action in the body needs it, and then
		// this check runs after the request's has ended. Any other spec
		// has no action, and must not read the field the request's check
		// may be writing at the same time.
		action := ""
		if after != nil {
			action = c.reqCheck.action
		}
		found := c.api.Spec.CheckReply(spec.Exchange{
			Method:      c.in.Method,
			EscapedPath: c.target.EscapedPath(),
			RawQuery:    c.target.RawQuery,
			Header:      c.in.Header,
			Action:      action,
			Status:      resp.StatusCode,
			ReplyHeader: resp.Header,
			ReplyBody:   c.respBody.copied(),
		})
		c.respBody.drop()
		return found
	})
}
