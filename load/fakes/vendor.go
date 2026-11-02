// Package fakes holds the two stand-ins at the proxy's edges in a load run:
// a vendor that answers fixed bodies, and a receiver for what the proxy
// exports. Neither keeps anything it is sent.
package fakes

import (
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
// its content type, and how long it waits before it answers.
type Shape struct {
	Body        []byte
	ContentType string
	Delay       time.Duration
}

// Shapes are the call shapes by name: a small JSON reply (an SSM
// GetParameter), a large one (about 115 KB, an SSM GetParametersByPath of
// 400 parameters), an XML reply (an S3 listing), and a small reply after a
// wait of 50 ms.
func Shapes() map[string]Shape {
	const json = "application/x-amz-json-1.1"
	small := []byte(`{"Parameter":{"Name":"/shop/db-url","Type":"String","Value":"postgres://db:5432/shop",` +
		`"Version":3,"ARN":"arn:aws:ssm:eu-west-1:192325781350:parameter/shop/db-url","DataType":"text"}}`)
	var large strings.Builder
	large.WriteString(`{"Parameters":[`)
	for i := range 400 {
		if i > 0 {
			large.WriteByte(',')
		}
		fmt.Fprintf(&large, `{"Name":"/shop/param-%d","Type":"String","Value":"value-%0200d","Version":1,"DataType":"text"}`, i, i)
	}
	large.WriteString(`]}`)
	var xml strings.Builder
	xml.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
		`<Name>shop-invoices</Name><Prefix></Prefix><KeyCount>50</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>`)
	for i := range 50 {
		fmt.Fprintf(&xml, `<Contents><Key>invoice-%04d.pdf</Key><LastModified>2026-11-02T10:00:00.000Z</LastModified>`+
			`<Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>`, i, 1000+i)
	}
	xml.WriteString(`</ListBucketResult>`)
	return map[string]Shape{
		"small-json": {Body: small, ContentType: json},
		"large-json": {Body: []byte(large.String()), ContentType: json},
		"xml":        {Body: []byte(xml.String()), ContentType: "application/xml"},
		"slow":       {Body: small, ContentType: json, Delay: 50 * time.Millisecond},
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
		_, _ = w.Write(s.Body)
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
