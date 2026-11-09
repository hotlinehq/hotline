// Package forward passes one call from an app on to its vendor, and the
// vendor's reply back to the app. It does what httputil.ReverseProxy does for
// the proxy, but copies the request once instead of five times (Peter,
// 2026-11-09), and fixes three of its bugs with trailers and early replies.
package forward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/http/httputil"
	"net/textproto"
	"strings"
	"sync"
)

// Forwarder passes one call on to the vendor and the reply back to the app.
//
// The app's request is never changed: the outgoing request is one shallow
// copy of it with a header map filled once, leaving out what must not travel
// onward, so the app's own headers stay whole for the record. Header value
// lists are shared between the two maps, not copied, and are cut to their
// length so an append to one never writes into the other.
//
// One Forwarder serves every call: what differs between calls comes in as
// Forward's parameters.
type Forwarder struct {
	// Buffers lends the buffer the reply body is copied through.
	Buffers httputil.BufferPool
}

// Errors that Forward returns, wrapped with detail.
var (
	errBrokenProtocol = errors.New("forward: a protocol name that is not plain text")
	errUnwritable     = errors.New("forward: an upgrade reply without a writable body")
	errUnasked        = errors.New("forward: the vendor switched to a protocol the app did not ask for")
)

// skipped says whether a header applies to one connection only (RFC 9110,
// section 7.6.1), or is one of the X-Forwarded ones, which are the proxy's
// to set, as with ReverseProxy.Rewrite.
func skipped(name string) bool {
	switch name {
	case "Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
		"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto":
		return true
	}
	return false
}

// hopHeaders are the headers that apply to one connection only.
func hopHeaders() [9]string {
	return [9]string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}
}

// forbiddenTrailer says whether a field must not be sent after the body
// (RFC 9110, section 6.5.1), as net/http lists them. Go's proxy passes them
// on (Go issues 82649 and 82450); this one drops them.
func forbiddenTrailer(name string) bool {
	switch name {
	case "Authorization", "Cache-Control", "Connection", "Content-Encoding",
		"Content-Length", "Content-Range", "Content-Type", "Expect", "Host",
		"Keep-Alive", "Max-Forwards", "Pragma", "Proxy-Authenticate",
		"Proxy-Authorization", "Proxy-Connection", "Range", "Realm", "Te",
		"Trailer", "Transfer-Encoding", "Www-Authenticate":
		return true
	}
	return false
}

// Forward sends in to the vendor through vendor, with the context ctx, which
// carries the call's cancel signal and any trace hooks, and writes the reply
// to w. vendor also sets what the outgoing request needs before it sends it:
// the vendor address, Host and any header the proxy adds. Forward never
// changes in. It returns an error only when no reply reached the app;
// the caller then answers the app itself.
func (f *Forwarder) Forward(ctx context.Context, w http.ResponseWriter, in *http.Request, vendor http.RoundTripper) error {
	var (
		mu   sync.Mutex
		done bool
	)
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		Got1xxResponse: func(code int, header textproto.MIMEHeader) error {
			mu.Lock()
			defer mu.Unlock()
			if done {
				return nil
			}
			// Hop-by-hop headers go no further on an early reply either
			// (Go issue 81319).
			dropHop(http.Header(header))
			h := w.Header()
			for k, v := range header {
				h[k] = v
			}
			w.WriteHeader(code)
			clear(h)
			return nil
		},
	})

	out := in.WithContext(ctx) // the one copy
	out.Close = false
	out.RequestURI = ""
	if in.ContentLength == 0 {
		out.Body = nil // a nil body lets the transport retry
	}
	if out.Body != nil {
		// The transport may still be reading the body when the vendor has
		// answered early; it must stop before the handler returns (Go issue
		// 46866), as ReverseProxy does.
		defer func() { _ = out.Body.Close() }()
	}
	out.Header = outgoing(in.Header)
	upType := upgradeType(in.Header)
	if !printable(upType) {
		return fmt.Errorf("%w: the app asked for %q", errBrokenProtocol, upType)
	}
	if upType != "" {
		out.Header.Set("Connection", "Upgrade")
		out.Header.Set("Upgrade", upType)
	}
	if hasToken(in.Header["Te"], "trailers") {
		out.Header.Set("Te", "trailers")
	}
	if _, ok := out.Header["User-Agent"]; !ok {
		// Stops the transport adding its own.
		out.Header["User-Agent"] = []string{""}
	}

	res, err := vendor.RoundTrip(out)
	mu.Lock()
	done = true
	mu.Unlock()
	if err != nil {
		return err
	}

	if res.StatusCode == http.StatusSwitchingProtocols {
		return f.upgrade(ctx, w, in, res)
	}
	dropHop(res.Header)
	h := w.Header()
	for k, v := range res.Header {
		if len(h[k]) == 0 {
			h[k] = v // shared, as the reply is ours
		} else {
			h[k] = append(h[k], v...)
		}
	}
	if len(res.Trailer) > 0 {
		keys := make([]string, 0, len(res.Trailer))
		for k := range res.Trailer {
			if !forbiddenTrailer(k) {
				keys = append(keys, k)
			}
		}
		if len(keys) > 0 {
			h.Add("Trailer", strings.Join(keys, ", "))
		}
	}
	w.WriteHeader(res.StatusCode)
	rc := http.NewResponseController(w)
	_ = rc.Flush()
	err = f.copy(w, rc, res.Body)
	_ = res.Body.Close()
	if err != nil {
		// Part of the reply is gone; the app must see a broken reply, not a
		// short one that looks whole.
		if in.Context().Value(http.ServerContextKey) != nil {
			panic(http.ErrAbortHandler)
		}
		return nil
	}
	if len(res.Trailer) > 0 {
		_ = rc.Flush()
	}
	for k, v := range res.Trailer {
		if !forbiddenTrailer(k) {
			h[http.TrailerPrefix+k] = v
		}
	}
	return nil
}

// outgoing is the header map of the outgoing request.
func outgoing(in http.Header) http.Header {
	out := make(http.Header, len(in))
	var listed []string
	for _, v := range in["Connection"] {
		for f := range strings.SplitSeq(v, ",") {
			if f = textproto.TrimString(f); f != "" {
				listed = append(listed, textproto.CanonicalMIMEHeaderKey(f))
			}
		}
	}
next:
	for k, v := range in {
		if skipped(k) {
			continue
		}
		for _, l := range listed {
			if l == k {
				continue next
			}
		}
		out[k] = v[:len(v):len(v)]
	}
	return out
}

func dropHop(h http.Header) {
	for _, v := range h["Connection"] {
		for f := range strings.SplitSeq(v, ",") {
			if f = textproto.TrimString(f); f != "" {
				h.Del(f)
			}
		}
	}
	for _, k := range hopHeaders() {
		h.Del(k)
	}
}

func upgradeType(h http.Header) string {
	if !hasToken(h["Connection"], "upgrade") {
		return ""
	}
	return h.Get("Upgrade")
}

// printable says whether a protocol name holds only visible ASCII
// characters and spaces, as ReverseProxy demands.
func printable(s string) bool {
	for i := range len(s) {
		if s[i] < ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}

func hasToken(values []string, token string) bool {
	for _, v := range values {
		for f := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(textproto.TrimString(f), token) {
				return true
			}
		}
	}
	return false
}

// copy writes the reply body, flushing after every part: a reply may be a
// stream.
func (f *Forwarder) copy(w io.Writer, rc *http.ResponseController, src io.Reader) error {
	buf := f.Buffers.Get()
	defer f.Buffers.Put(buf)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if err := rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
				return err
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil
			}
			return rerr
		}
	}
}

// upgrade passes an upgraded connection, such as a WebSocket, both ways until
// either side ends. It returns an error only when the app got no answer.
func (f *Forwarder) upgrade(ctx context.Context, w http.ResponseWriter, in *http.Request, res *http.Response) error {
	vendor, ok := res.Body.(io.ReadWriteCloser)
	if !ok {
		return errUnwritable
	}
	defer func() { _ = vendor.Close() }()
	resType := upgradeType(res.Header)
	if !printable(resType) {
		return fmt.Errorf("%w: the vendor switched to %q", errBrokenProtocol, resType)
	}
	if upgradeType(in.Header) == "" || !strings.EqualFold(resType, upgradeType(in.Header)) {
		return fmt.Errorf("%w: %q", errUnasked, resType)
	}
	h := w.Header()
	for k, v := range res.Header {
		h[k] = v
	}
	app, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return err
	}
	defer func() { _ = app.Close() }()
	// The call ending, such as on the vendor timeout, ends the upgraded
	// connection too, as with ReverseProxy.
	stop := context.AfterFunc(ctx, func() { _ = vendor.Close() })
	defer stop()
	res.Header = h
	res.Body = nil
	if res.Write(brw) != nil || brw.Flush() != nil {
		return nil // the app has gone; nothing is left to answer
	}
	ended := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(vendor, brw); ended <- struct{}{} }()
	go func() { _, _ = io.Copy(app, vendor); ended <- struct{}{} }()
	<-ended
	return nil
}
