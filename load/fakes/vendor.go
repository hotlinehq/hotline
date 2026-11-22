// Package fakes holds the two stand-ins at the proxy's edges in a load run:
// a vendor that answers fixed bodies, and a receiver for what the proxy
// exports. Neither keeps anything it is sent.
package fakes

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"
)

// Shape is one kind of call the vendor answers: the body it sends back,
// its content type, and how long it waits before it answers. A streamed
// shape also has Events, the body cut in the pieces the vendor sends one
// at a time.
type Shape struct {
	Body        []byte
	ContentType string
	Delay       time.Duration
	Events      [][]byte
}

// anthropicStream is Anthropic's streamed answer of n events, as its
// streaming docs show it: the start of the message and of its one block of
// text, a delta of a few words for each event in between, then the ends.
func anthropicStream(n int) [][]byte {
	event := func(name, data string) []byte { return []byte("event: " + name + "\ndata: " + data + "\n\n") }
	events := [][]byte{
		event("message_start", `{"type":"message_start","message":{"id":"msg_01load","type":"message","role":"assistant","content":[],`+
			`"model":"claude-opus-5-5","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":25,"output_tokens":1}}}`),
		event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
	}
	for i := range n - 5 {
		events = append(events, event("content_block_delta",
			fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"word %03d of the answer "}}`, i)))
	}
	return append(events,
		event("content_block_stop", `{"type":"content_block_stop","index":0}`),
		event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":95}}`),
		event("message_stop", `{"type":"message_stop"}`))
}

// parameters is the body of an SSM GetParametersByPath response with n
// parameters, each about 290 bytes.
func parameters(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"Parameters":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"Name":"/shop/param-%d","Type":"String","Value":"value-%0200d","Version":1,"DataType":"text"}`, i, i)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

// Shapes are the call shapes by name: a small JSON response (an SSM
// GetParameter), JSON responses of about 30 KB, 115 KB and 500 KB (an SSM
// GetParametersByPath of 105, 400 and 1780 parameters), an XML response of
// about 7 KB (an S3 ListObjectsV2 of 50 files), a small response after a
// wait of 50 ms, and an Anthropic answer streamed as 100 Server-Sent Events
// (about 13 KB).
func Shapes() map[string]Shape {
	const json = "application/x-amz-json-1.1"
	small := []byte(`{"Parameter":{"Name":"/shop/db-url","Type":"String","Value":"postgres://db:5432/shop",` +
		`"Version":3,"ARN":"arn:aws:ssm:eu-west-1:192325781350:parameter/shop/db-url","DataType":"text"}}`)
	var xml strings.Builder
	xml.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
		`<Name>shop-invoices</Name><Prefix></Prefix><KeyCount>50</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>`)
	for i := range 50 {
		fmt.Fprintf(&xml, `<Contents><Key>invoice-%04d.pdf</Key><LastModified>2026-11-02T10:00:00.000Z</LastModified>`+
			`<Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>`, i, 1000+i)
	}
	xml.WriteString(`</ListBucketResult>`)
	stream := anthropicStream(100)
	return map[string]Shape{
		"stream-sse":  {Body: bytes.Join(stream, nil), ContentType: "text/event-stream", Events: stream},
		"small-json":  {Body: small, ContentType: json},
		"medium-json": {Body: parameters(105), ContentType: json},
		"large-json":  {Body: parameters(400), ContentType: json},
		"huge-json":   {Body: parameters(1780), ContentType: json},
		"xml":         {Body: []byte(xml.String()), ContentType: "application/xml"},
		"slow":        {Body: small, ContentType: json, Delay: 50 * time.Millisecond},
	}
}

// Vendor answers every call with one shape, on TLS with HTTP/2 and on plain
// HTTP (the call without the proxy).
type Vendor struct {
	// TLSAddr and PlainAddr are host:port.
	TLSAddr   string
	PlainAddr string
	// CertPEM is the certificate behind TLSAddr, for whoever must trust it.
	CertPEM []byte
	servers []*http.Server
}

// StartVendor starts the vendor on free ports of 127.0.0.1.
func StartVendor(s Shape) (*Vendor, error) {
	certPEM, cert, err := selfSigned()
	if err != nil {
		return nil, err
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(s.Delay)
		w.Header().Set("Content-Type", s.ContentType)
		if s.Events == nil {
			_, _ = w.Write(s.Body)
			return
		}
		// Each event leaves on its own, as a model sends its answer.
		flush := http.NewResponseController(w).Flush
		for _, e := range s.Events {
			if _, err := w.Write(e); err != nil || flush() != nil {
				return
			}
		}
	})
	v := &Vendor{CertPEM: certPEM}
	for _, secure := range []bool{true, false} {
		ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			v.Close()
			return nil, err
		}
		srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
		if secure {
			srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}, MinVersion: tls.VersionTLS12}
			v.TLSAddr = ln.Addr().String()
			go func() { _ = srv.ServeTLS(ln, "", "") }()
		} else {
			v.PlainAddr = ln.Addr().String()
			go func() { _ = srv.Serve(ln) }()
		}
		v.servers = append(v.servers, srv)
	}
	return v, nil
}

// Close stops the vendor.
func (v *Vendor) Close() {
	for _, s := range v.servers {
		_ = s.Close()
	}
}

// selfSigned makes a certificate for localhost and 127.0.0.1.
func selfSigned() ([]byte, tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, tls.Certificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "hotline load test vendor"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, tls.Certificate{}, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
