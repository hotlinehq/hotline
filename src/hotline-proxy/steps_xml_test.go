package proxy_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cucumber/godog"
)

const filesHost = "api.files.test"

func xmlSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the XML files spec$`, func() error {
		spec, err := os.ReadFile(filepath.Join("testdata", "xml", "files.openapi.yaml"))
		w.addIntegration(integration("talon-shop-a", w.specAPI(filesHost, string(spec), "")))
		return err
	})
	sc.Step(`^the files api with a spec whose shipment body is (\S+) with (x-hotline-body: \S+)$`, func(mediaType, mark string) error {
		spec := "openapi: 3.1.0\ninfo: { title: Files, version: '1' }\npaths:\n  /shipments:\n    post:\n" +
			"      requestBody:\n        content:\n          " + mediaType + ":\n            " + mark + "\n" +
			"            schema: { type: object }\n" +
			"      responses:\n        '201': { description: done }\n"
		w.addIntegration(integration("talon-shop-a", w.specAPI(filesHost, spec, "")))
		return nil
	})
	sc.Step(`^the files vendor answers (\d+) with the (\S+) body (.*)$`, func(status int, mediaType, body string) error {
		w.filesAnswer(status, mediaType, body)
		return nil
	})
	sc.Step(`^the files vendor answers (\d+) with an application/xml list of (\d+) MiB$`, func(status, mib int) error {
		var b strings.Builder
		b.WriteString("<ListBucketResult><Name>shop-invoices</Name><KeyCount>x</KeyCount>")
		for i := 0; b.Len() < mib<<20; i++ {
			b.WriteString("<Contents><Key>A-" + strconv.Itoa(i) + ".pdf</Key><Size>1</Size></Contents>")
		}
		b.WriteString("</ListBucketResult>")
		w.filesAnswer(status, "application/xml", b.String())
		return nil
	})
	sc.Step(`^the files vendor answers (\d+) with a list nested (\d+) elements deep$`, func(status, depth int) error {
		// The root and Owner, then elements Owner holds, each inside the last.
		body := "<ListBucketResult><Name>shop-invoices</Name><KeyCount>0</KeyCount><Owner>" +
			strings.Repeat("<a>", depth-2) + strings.Repeat("</a>", depth-2) + "</Owner></ListBucketResult>"
		w.filesAnswer(status, "application/xml", body)
		return nil
	})
	sc.Step(`^the app sends (GET|POST|PUT) (\S+) to the files vendor(?: with the (\S+) body (.*))?$`, func(method, path, mediaType, body string) error {
		var headers []string
		if w.appID != "" {
			headers = append(headers, basic(w.appID+":x"))
		}
		r := request{method: method, target: "https://" + filesHost + path, length: -1}
		if mediaType != "" {
			headers = append(headers, "Content-Type: "+mediaType)
			r.body, r.length = strings.NewReader(body), len(body)
		}
		r.headers = headers
		return w.send(r)
	})
}

// filesAnswer makes the files vendor answer every call with one reply.
func (w *world) filesAnswer(status int, mediaType, body string) {
	w.vendor(filesHost).setAnswer(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		rw.Header().Set("Content-Type", mediaType)
		rw.WriteHeader(status)
		_, _ = rw.Write([]byte(body))
	})
}
