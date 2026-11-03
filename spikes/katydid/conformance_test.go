package katydidcheck_test

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	katydidcheck "hotline-spike-katydid"
)

// The W3C XML Conformance Test Suite (https://www.w3.org/XML/Test/), cut down by manifest.py to the cases
// we can read: XML 1.0, UTF-8, no DOCTYPE (we refuse those on purpose), no entity declarations.
// Well-formed bodies must read without an error; not-well-formed bodies must give one.
// Set XMLCONF_MANIFEST to the manifest file to run it; it is skipped otherwise. See README.md.
// The streaming reader must get every case right. The tree reader is only reported (Go's XML reader is lenient).
type confCase struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Expect string `json:"expect"`
	Sec    string `json:"sec"`
}

var _ = Describe("W3C XML conformance suite", func() {
	path := os.Getenv("XMLCONF_MANIFEST")
	if path == "" {
		return
	}
	var cases []confCase
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	Expect(json.Unmarshal(data, &cases)).To(Succeed())

	for name, check := range map[string]func(*katydidcheck.Checker, []byte) (bool, error){
		"tree reader":      (*katydidcheck.Checker).CheckXML,
		"streaming reader": (*katydidcheck.Checker).CheckXMLStream,
	} {
		It(name+" agrees with the suite", func() {
			c, err := katydidcheck.Compile([]byte(`{"type":"object"}`))
			Expect(err).NotTo(HaveOccurred())
			var wrong []string
			for _, tc := range cases {
				body, err := os.ReadFile(tc.Path)
				Expect(err).NotTo(HaveOccurred())
				_, rerr := check(c, body)
				switch {
				case tc.Expect == "wf" && rerr != nil:
					wrong = append(wrong, fmt.Sprintf("refused a well-formed body: %s (section %s): %v", tc.ID, tc.Sec, rerr))
				case tc.Expect == "notwf" && rerr == nil:
					wrong = append(wrong, fmt.Sprintf("accepted a not well-formed body: %s (section %s)", tc.ID, tc.Sec))
				}
			}
			sort.Strings(wrong)
			GinkgoWriter.Printf("%d of %d cases wrong\n%s\n", len(wrong), len(cases), strings.Join(wrong, "\n"))
			fmt.Printf("\n%s: %d of %d cases wrong\n%s\n", name, len(wrong), len(cases), strings.Join(wrong, "\n"))
			if name == "streaming reader" {
				Expect(wrong).To(BeEmpty())
			}
		})
	}
})
