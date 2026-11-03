package katydidcheck

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// xnode is a plain element tree read with Go's standard XML reader.
type xnode struct {
	name     string
	text     strings.Builder
	children []*xnode
}

func readXML(data []byte) (*xnode, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true
	var stack []*xnode
	var root *xnode
	for {
		t, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := t.(type) {
		case xml.StartElement:
			n := &xnode{name: t.Name.Local}
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
					continue
				}
				c := &xnode{name: a.Name.Local}
				c.text.WriteString(a.Value)
				n.children = append(n.children, c)
			}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.children = append(p.children, n)
			} else if root == nil {
				root = n
			} else {
				return nil, errors.New("two roots")
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text.Write(t)
			}
		case xml.Directive:
			return nil, errors.New("doctype refused")
		}
	}
	if root == nil || len(stack) != 0 {
		return nil, errors.New("not well formed")
	}
	return root, nil
}
