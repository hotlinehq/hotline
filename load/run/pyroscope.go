package run

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Pyroscope is where the load test sends the profiles it saves, Grafana
// Cloud's store for profiles. Only the load test does this: the proxy itself
// sends its profiles nowhere.
type Pyroscope struct {
	URL, Username, Password string
}

// PyroscopeFromEnv says where profiles go, from GRAFANA_PYROSCOPE_URL (the
// address of the Pyroscope stack), GRAFANA_PYROSCOPE_USERNAME (its user id)
// and GRAFANA_PYROSCOPE_PASSWORD (a token that may write profiles). Without
// the address it is nil, and nothing is sent.
func PyroscopeFromEnv(getenv func(string) string) *Pyroscope {
	address := getenv("GRAFANA_PYROSCOPE_URL")
	if address == "" {
		return nil
	}
	return &Pyroscope{URL: address, Username: getenv("GRAFANA_PYROSCOPE_USERNAME"), Password: getenv("GRAFANA_PYROSCOPE_PASSWORD")}
}

// PyroscopeName is the name a run's profiles are stored under: the program
// and the labels that tell runs apart, the same ones the proxy's numbers carry.
func PyroscopeName(t PushTags) string {
	labels := []string{fmt.Sprintf("shape=%q", t.Shape), fmt.Sprintf("rate=%q", strconv.Itoa(t.Rate))}
	if t.Label != "" {
		labels = append(labels, fmt.Sprintf("label=%q", t.Label))
	}
	return "hotline-proxy{" + strings.Join(labels, ",") + "}"
}

// Upload sends one saved profile file (Go's pprof format) as the profile of
// the proxy over the time from start to end.
func (p *Pyroscope) Upload(file, name string, start, end time.Time) error {
	raw, err := os.ReadFile(file) //nolint:gosec // a file in the run folder
	if err != nil {
		return err
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("profile", "profile.pprof")
	if err != nil {
		return err
	}
	if _, err := part.Write(raw); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return err
	}
	query := url.Values{"name": {name}, "from": {strconv.FormatInt(start.Unix(), 10)}, "until": {strconv.FormatInt(end.Unix(), 10)}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.URL, "/")+"/ingest?"+query.Encode(), &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	if p.Username != "" {
		req.SetBasicAuth(p.Username, p.Password)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		reason, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("pyroscope answered %s: %s", resp.Status, strings.TrimSpace(string(reason)))
	}
	return nil
}
