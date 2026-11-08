package spec_test

import (
	"net/http"
	"os"
	"testing"

	"hotline-proxy/internal/spec"
)

// These measure one search for a call's route in our two largest AWS
// specs: SSM's 152 routes, picked by the X-Amz-Target header, and S3's 65,
// picked by path and query (Peter, 2026-11-08: whether finding the route as
// the call starts would slow the call down).

func parsedFile(b *testing.B, read func() ([]byte, error)) *spec.Spec {
	b.Helper()
	data, err := read()
	if err != nil {
		b.Fatal(err)
	}
	s, err := spec.Parse("bench.yaml", data, acceptAll)
	if err != nil {
		b.Fatal(err)
	}
	return s
}

func BenchmarkFindSSM(b *testing.B) {
	s := parsedFile(b, func() ([]byte, error) { return os.ReadFile("../../../../specs/aws/ssm/2014-11-06.openapi.yaml") })
	e := spec.Exchange{Method: "POST", EscapedPath: "/", Header: http.Header{"X-Amz-Target": {"AmazonSSM.GetParameter"}}}
	b.ReportAllocs()
	for b.Loop() {
		s.Find(e)
	}
}

func BenchmarkFindS3(b *testing.B) {
	s := parsedFile(b, func() ([]byte, error) { return os.ReadFile("../../../../specs/aws/s3/2006-03-01.openapi.yaml") })
	e := spec.Exchange{Method: "GET", EscapedPath: "/my-bucket/some/key.txt", RawQuery: "versionId=1"}
	b.ReportAllocs()
	for b.Loop() {
		s.Find(e)
	}
}
