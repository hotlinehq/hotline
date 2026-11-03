package katydidcheck_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	katydidcheck "hotline-spike-katydid"
	"os"
	"strings"
	"testing"

	santhosh "github.com/santhosh-tekuri/jsonschema/v6"
)

const dir = "testdata/"

func jsonBody(n int) []byte {
	var j strings.Builder
	j.WriteString(`{"Parameters":[`)
	for i := range n {
		if i > 0 {
			j.WriteByte(',')
		}
		fmt.Fprintf(&j, `{"Name":"/shop/param-%d","Type":"String","Value":"value-%0200d","Version":1,"DataType":"text","LastModifiedDate":1.7e9}`, i, i)
	}
	j.WriteString(`]}`)
	return []byte(j.String())
}

func xmlBody(n int) []byte {
	var x strings.Builder
	x.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>shop-invoices</Name><Prefix></Prefix><KeyCount>50</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>`)
	for i := range n {
		fmt.Fprintf(&x, `<Contents><Key>invoice-%04d-with-a-long-name-to-fill-the-reply-%0100d.pdf</Key><LastModified>2026-11-02T10:00:00.000Z</LastModified><Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>`, i, i, 1000+i)
	}
	x.WriteString(`</ListBucketResult>`)
	return []byte(x.String())
}

func schemaOf(b testing.TB, f string) []byte {
	d, err := os.ReadFile(dir + f)
	if err != nil {
		b.Fatal(err)
	}
	return d
}

func santhoshCompile(b testing.TB, doc []byte) *santhosh.Schema {
	c := santhosh.NewCompiler()
	var d any
	_ = json.Unmarshal(doc, &d)
	_ = c.AddResource("s.json", d)
	s, err := c.Compile("s.json")
	if err != nil {
		b.Fatal(err)
	}
	return s
}

func benchSanthosh(b *testing.B, sch, body []byte) {
	s := santhoshCompile(b, sch)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		inst, _ := santhosh.UnmarshalJSON(bytes.NewReader(body))
		if err := s.Validate(inst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSON_Santhosh(b *testing.B) {
	benchSanthosh(b, schemaOf(b, "ssm.schema.json"), jsonBody(400))
}

func BenchmarkJSON_Katydid(b *testing.B) {
	body := jsonBody(400)
	c, err := katydidcheck.Compile(schemaOf(b, "ssm.schema.json"))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		if ok, _ := c.CheckJSON(body); !ok {
			b.Fatal("not valid")
		}
	}
}

func BenchmarkXML_Katydid(b *testing.B) {
	body := xmlBody(450)
	c, err := katydidcheck.Compile(schemaOf(b, "s3.schema.json"))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		if ok, err := c.CheckXML(body); !ok {
			b.Fatal("not valid", err)
		}
	}
}
