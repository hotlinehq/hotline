package run_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-load/run"
)

var _ = Describe("Sending profiles to Pyroscope", func() {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	It("sends nothing unless the address is set", func() {
		Expect(run.PyroscopeFromEnv(env(nil))).To(BeNil())
		Expect(run.PyroscopeFromEnv(env(map[string]string{"GRAFANA_PYROSCOPE_USERNAME": "7"}))).To(BeNil())
	})

	It("takes the address, user and password from the environment", func() {
		got := run.PyroscopeFromEnv(env(map[string]string{
			"GRAFANA_PYROSCOPE_URL": "https://p.example", "GRAFANA_PYROSCOPE_USERNAME": "7", "GRAFANA_PYROSCOPE_PASSWORD": "token",
		}))
		Expect(got).To(Equal(&run.Pyroscope{URL: "https://p.example", Username: "7", Password: "token"}))
	})

	It("names the profiled program after the shape, rate and label", func() {
		Expect(run.PyroscopeName(run.PushTags{Shape: "small-json", Rate: 5000, Label: "main"})).To(
			Equal(`hotline-proxy{shape="small-json",rate="5000",label="main"}`))
		Expect(run.PyroscopeName(run.PushTags{Shape: "xml", Rate: 500})).To(Equal(`hotline-proxy{shape="xml",rate="500"}`))
	})

	It("uploads a profile file with the name and the time span, signed in with the user and password", func() {
		var path, name, from, until, user, pass, field, body string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path, name = r.URL.Path, r.URL.Query().Get("name")
			from, until = r.URL.Query().Get("from"), r.URL.Query().Get("until")
			user, pass, _ = r.BasicAuth()
			Expect(r.ParseMultipartForm(1 << 20)).To(Succeed())
			for key, files := range r.MultipartForm.File {
				field = key
				f, err := files[0].Open()
				Expect(err).NotTo(HaveOccurred())
				raw, _ := io.ReadAll(f)
				body = string(raw)
			}
		}))
		defer server.Close()
		file := filepath.Join(GinkgoT().TempDir(), "cpu.pprof")
		Expect(os.WriteFile(file, []byte("profile bytes"), 0o600)).To(Succeed())

		p := &run.Pyroscope{URL: server.URL + "/", Username: "7", Password: "token"}
		start := time.Unix(1000, 0)
		Expect(p.Upload(file, "hotline-proxy", start, start.Add(10*time.Second))).To(Succeed())

		Expect(path).To(Equal("/ingest"))
		Expect(name).To(Equal("hotline-proxy"))
		Expect(from).To(Equal("1000"))
		Expect(until).To(Equal("1010"))
		Expect([]string{user, pass}).To(Equal([]string{"7", "token"}))
		Expect(field).To(Equal("profile"))
		Expect(body).To(Equal("profile bytes"))
	})

	It("says so when Pyroscope refuses", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "no", http.StatusUnauthorized)
		}))
		defer server.Close()
		file := filepath.Join(GinkgoT().TempDir(), "cpu.pprof")
		Expect(os.WriteFile(file, []byte("x"), 0o600)).To(Succeed())
		err := (&run.Pyroscope{URL: server.URL}).Upload(file, "n", time.Now(), time.Now())
		Expect(err).To(MatchError(ContainSubstring("401")))
	})
})
