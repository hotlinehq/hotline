package katydidcheck_test

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	katydidcheck "hotline-proxy/internal/katydidcheck"
)

// The W3C XML Conformance Test Suite (https://www.w3.org/XML/Test/, file xmlts20130923.tar.gz), cut down to the
// cases we can read: XML 1.0 fifth edition, UTF-8, no DOCTYPE (we refuse those on purpose), no entity declarations,
// and no namespace rules (we ignore namespaces on purpose).
// Well-formed bodies must read without an error; not-well-formed bodies must give one.
// Set XMLCONF_DIR to the unpacked "xmlconf" folder to run it; it is skipped otherwise. See README.md.
// The reader must get every case right.

type confCase struct {
	id, path, section string
	wellFormed        bool
}

var (
	systemEntity = regexp.MustCompile(`<!ENTITY\s+\S+\s+SYSTEM\s+"([^"]+)"`)
	doctypeRe    = regexp.MustCompile(`(?s)<!DOCTYPE[^\[>]*(\[.*?\])?\s*>`)
	xmlDeclRe    = regexp.MustCompile(`^\s*<\?xml[^>]*\?>`)
	encodingRe   = regexp.MustCompile(`^(\xef\xbb\xbf)?\s*<\?xml[^>]*encoding\s*=\s*["']([^"']+)`)
)

// loadCases reads the suite's index files and returns the cases we can use, and how many were left out for each reason.
func loadCases(dir string) ([]confCase, map[string]int) {
	index, err := os.ReadFile(filepath.Join(dir, "xmlconf.xml")) //nolint:gosec // a test file
	Expect(err).NotTo(HaveOccurred())
	left := map[string]int{}
	var cases []confCase
	for _, m := range systemEntity.FindAllSubmatch(index, -1) {
		coll := string(m[1])
		raw, err := os.ReadFile(filepath.Join(dir, coll)) //nolint:gosec // a test file
		if err != nil {
			continue
		}
		raw = doctypeRe.ReplaceAll(raw, nil)
		raw = append(append([]byte("<R>"), xmlDeclRe.ReplaceAll(raw, nil)...), "</R>"...)
		dec := xml.NewDecoder(bytes.NewReader(raw)) //nolint:gosec // a test file
		var bases []string
		for {
			tok, err := dec.Token()
			if errors.Is(err, io.EOF) {
				break
			}
			Expect(err).NotTo(HaveOccurred(), coll)
			switch t := tok.(type) {
			case xml.StartElement:
				attr := map[string]string{}
				for _, a := range t.Attr {
					attr[a.Name.Local] = a.Value
				}
				if t.Name.Local != "TEST" {
					bases = append(bases, attr["base"])
					continue
				}
				bases = append(bases, "")
				file := filepath.Join(dir, filepath.Dir(coll), filepath.Join(bases...), attr["URI"])
				ents, version, edition := attr["ENTITIES"], attr["VERSION"], attr["EDITION"]
				body, err := os.ReadFile(file) //nolint:gosec // a test file
				switch {
				case version != "" && version != "1.0":
					left["xml 1.1"]++
				case edition != "" && !strings.Contains(" "+edition+" ", " 5 "):
					left["older XML edition rules"]++
				case strings.Contains(coll, "namespaces") && (attr["TYPE"] == "not-wf" || attr["TYPE"] == "error"):
					left["namespace rules (ignored on purpose)"]++
				case ents != "" && ents != "none":
					left["needs entity declarations"]++
				case err != nil:
					left["missing file"]++
				case bytes.HasPrefix(body, []byte{0xff, 0xfe}) || bytes.HasPrefix(body, []byte{0xfe, 0xff}) || bytes.Contains(body[:min(len(body), 4)], []byte{0}):
					left["utf-16 or utf-32"]++
				case !utf8.Valid(body):
					left["not utf-8"]++
				case encodingRe.Match(body) && !strings.EqualFold(strings.ReplaceAll(string(encodingRe.FindSubmatch(body)[2]), "-", ""), "utf8"):
					left["non-utf-8 encoding"]++
				case bytes.Contains(body, []byte("<!DOCTYPE")):
					left["has DOCTYPE (refused on purpose)"]++
				default:
					cases = append(cases, confCase{id: attr["ID"], path: file, section: attr["SECTIONS"], wellFormed: attr["TYPE"] != "not-wf"})
				}
			case xml.EndElement:
				bases = bases[:len(bases)-1]
			}
		}
	}
	return cases, left
}

var _ = Describe("W3C XML conformance suite", func() {
	dir := os.Getenv("XMLCONF_DIR")
	if dir == "" {
		return
	}

	It("the XML reader agrees with the suite", func() {
		cases, left := loadCases(dir)
		Expect(len(cases)).To(BeNumerically(">", 200))
		c, err := katydidcheck.Compile([]byte(`{"type":"object"}`))
		Expect(err).NotTo(HaveOccurred())
		var wrong []string
		for _, tc := range cases {
			body, err := os.ReadFile(tc.path)
			Expect(err).NotTo(HaveOccurred())
			_, rerr := c.CheckXML(body)
			switch {
			case tc.wellFormed && rerr != nil:
				wrong = append(wrong, fmt.Sprintf("refused a well-formed body: %s (section %s): %v", tc.id, tc.section, rerr))
			case !tc.wellFormed && rerr == nil:
				wrong = append(wrong, fmt.Sprintf("accepted a not well-formed body: %s (section %s)", tc.id, tc.section))
			}
		}
		sort.Strings(wrong)
		fmt.Printf("\n%d of %d cases wrong (left out: %v)\n%s\n", len(wrong), len(cases), left, strings.Join(wrong, "\n"))
		Expect(wrong).To(BeEmpty())
	})
})
