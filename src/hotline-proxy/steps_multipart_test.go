package proxy_test

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cucumber/godog"
)

// openAIAudioSpec is the OpenAI audio spec as the repository holds it,
// written by hand.
const openAIAudioSpec = "../../specs/openai/audio/2026-11-24.openapi.yaml"

// openAILogin is the app's made-up OpenAI key, which the spec's security
// section asks for.
const openAILogin = "Authorization: Bearer sk-not-a-real-key"

// newStripeFile is Stripe's answer to a file upload, as its docs show it.
const newStripeFile = `{"id":"file_1Mr5","object":"file","created":1700000000,"filename":"receipt.pdf","purpose":"dispute_evidence","size":307200,"type":"pdf"}`

// upload is the multipart body the app sent last, kept to compare with
// what the vendor got.
type upload struct {
	sent []byte
	got  chan []byte
}

func multipartSteps(sc *godog.ScenarioContext, w *world) {
	u := &upload{got: make(chan []byte, 1)}
	// answer answers every call with status, contentType and body, and keeps
	// the body the vendor got.
	answer := func(host, contentType, body string) {
		w.vendor(host).setAnswer(func(rw http.ResponseWriter, r *http.Request) {
			data, _ := io.ReadAll(r.Body)
			select {
			case u.got <- data:
			default:
			}
			rw.Header().Set("Content-Type", contentType)
			_, _ = rw.Write([]byte(body))
		})
	}
	sc.Step(`^the OpenAI audio spec from specs(, with the file property marked x-hotline-redact false)?(, with content full(?: up to (\d+) bytes)? and a file exporter)?$`, func(marked, full, limit string) error {
		path, extra := openAIAudioSpec, ""
		if marked != "" {
			shown, err := markFileShown(w.dir)
			if err != nil {
				return err
			}
			path = shown
		}
		if full != "" {
			extra = w.keepInFull(limit)
		}
		if err := w.useRepositorySpec("api.openai.com", path, http.StatusOK, "application/json", `{"text":"Hello."}`, extra); err != nil {
			return err
		}
		answer("api.openai.com", "application/json", `{"text":"Hello."}`)
		return nil
	})
	sc.Step(`^OpenAI with no spec, with content full and a file exporter$`, func() error {
		// The integration's only api is another vendor's, so OpenAI's
		// calls pass with no spec.
		w.addIntegration(integration("talon-shop-a", w.talonAPI("")) + w.keepInFull(""))
		answer("api.openai.com", "application/json", `{"text":"Hello."}`)
		return nil
	})
	sc.Step(`^the Stripe spec from specs, with Stripe answering a new file$`, func() error {
		if err := w.useRepositorySpec("files.stripe.com", stripeSpec, http.StatusOK, "application/json", newStripeFile, ""); err != nil {
			return err
		}
		answer("files.stripe.com", "application/json", newStripeFile)
		return nil
	})
	sc.Step(`^the app uploads (\d+) (KiB|MiB) of (\S+) named "([^"]*)" to (OpenAI|Stripe)'s (/\S+) with the text parts "([^"]*)"$`, func(n int, unit, fileType, fileName, vendor, path, parts string) error {
		size := n << 10
		if unit == "MiB" {
			size = n << 20
		}
		body := newMultipart()
		body.file("file", fileName, fileType, size)
		if err := body.texts(parts); err != nil {
			return err
		}
		return w.upload(u, vendor, path, body)
	})
	sc.Step(`^the app posts to OpenAI's (/\S+) only the text parts "([^"]*)"$`, func(path, parts string) error {
		body := newMultipart()
		if err := body.texts(parts); err != nil {
			return err
		}
		return w.upload(u, "OpenAI", path, body)
	})
	sc.Step(`^the app uploads (\d+) KiB of (\S+) named "([^"]*)" to OpenAI's (/\S+) with a prompt of (\d+) MiB and no model$`, func(n int, fileType, fileName, path string, mib int) error {
		body := newMultipart()
		body.file("file", fileName, fileType, n<<10)
		body.text("prompt", strings.Repeat("Say it slowly. ", (mib<<20)/15+1))
		return w.upload(u, "OpenAI", path, body)
	})
	sc.Step(`^the app starts a voice call at OpenAI's (/\S+) with the session (.+?)( twice)?$`, func(path, session, twice string) error {
		body := newMultipart()
		body.text("sdp", "v=0")
		for range 1 + len(strings.Fields(twice)) {
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", `form-data; name="session"`)
			h.Set("Content-Type", "application/json")
			part, _ := body.w.CreatePart(h)
			_, _ = part.Write([]byte(session))
		}
		return w.upload(u, "OpenAI", path, body)
	})
	sc.Step(`^the app posts to OpenAI's (/\S+) a multipart body whose boundary never comes$`, func(path string) error {
		u.sent = []byte("model=gpt-4o-transcribe\r\n")
		return w.post("https://api.openai.com"+path, "multipart/form-data; boundary=never-sent", string(u.sent), openAILogin)
	})
	sc.Step(`^the app posts to OpenAI's (/\S+) a multipart body that ends in the middle of the file$`, func(path string) error {
		body := newMultipart()
		body.text("model", "gpt-4o-transcribe")
		body.file("file", "memo.mp3", "audio/mpeg", 4<<10)
		u.sent = body.buf.Bytes()
		return w.post("https://api.openai.com"+path, body.w.FormDataContentType(), string(u.sent), openAILogin)
	})
	sc.Step(`^the app posts to OpenAI's (/\S+) a multipart/mixed body$`, func(path string) error {
		body := newMultipart()
		body.text("model", "gpt-4o-transcribe")
		body.close()
		u.sent = body.buf.Bytes()
		mixed := "multipart/mixed; boundary=" + body.w.Boundary()
		return w.post("https://api.openai.com"+path, mixed, string(u.sent), openAILogin)
	})
	sc.Step(`^the app posts to OpenAI's (/\S+) the body "(.*)" as "([^"]*)"$`, func(path, written, contentType string) error {
		// A \n written in the body is a line feed alone.
		body := strings.ReplaceAll(strings.ReplaceAll(written, "~", "\r\n"), `\n`, "\n")
		u.sent = []byte(body)
		return w.post("https://api.openai.com"+path, contentType, body, openAILogin)
	})
	sc.Step(`^the app posts to OpenAI's (/\S+) a part whose headers are over 16 KiB$`, func(path string) error {
		body := newMultipart()
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="model"`)
		h.Set("X-Note", strings.Repeat("n", 17<<10))
		part, _ := body.w.CreatePart(h)
		_, _ = part.Write([]byte("gpt-4o-transcribe"))
		return w.upload(u, "OpenAI", path, body)
	})
	sc.Step(`^the app uploads (\d+) KiB of audio sent as "([^"]*)" to OpenAI's (/\S+) with the text parts "([^"]*)"$`, func(n int, fileType, path, parts string) error {
		body := newMultipart()
		body.file("file", "memo.mp3", fileType, n<<10)
		if err := body.texts(parts); err != nil {
			return err
		}
		return w.upload(u, "OpenAI", path, body)
	})
	sc.Step(`^the app uploads (\d+) KiB of audio to OpenAI's (/\S+) with the text parts "([^"]*)" and (\d+) KiB after the closing boundary$`, func(n int, path, parts string, after int) error {
		body := newMultipart()
		body.file("file", "memo.mp3", "audio/mpeg", n<<10)
		if err := body.texts(parts); err != nil {
			return err
		}
		body.close()
		body.buf.WriteString(strings.Repeat("epilogue ", (after<<10)/9))
		return w.upload(u, "OpenAI", path, body)
	})
	sc.Step(`^the vendor got the upload byte for byte$`, func() error {
		got := <-u.got
		if !bytes.Equal(got, u.sent) {
			return failf("the vendor got %d bytes that differ from the %d the app sent", len(got), len(u.sent))
		}
		return nil
	})
	sc.Step(`^the record lists the file part (\S+): (\d+) bytes of (\S+) named (\S+)$`, func(name, size, fileType, fileName string) error {
		record, err := w.record()
		if err != nil {
			return err
		}
		list, ok := record["hotline.request.files"].([]any)
		if !ok || len(list) != 1 {
			return failf("the record's hotline.request.files is %v, want one file part", record["hotline.request.files"])
		}
		got := fmt.Sprint(list[0])
		want := fmt.Sprint(map[string]any{"name": name, "size": size, "content_type": fileType, "file_name": fileName})
		if got != want {
			return failf("the file part is %s, want %s", got, want)
		}
		return nil
	})
	sc.Step(`^no record holds "([^"]*)"$`, func(text string) error {
		for _, r := range w.records() {
			if strings.Contains(sprint(r.attrs), text) {
				return failf("a record holds %q: %v", text, r.attrs)
			}
		}
		return nil
	})
	sc.Step(`^the line's request body is the upload (with the file name REDACTED|as sent)$`, func(how string) error {
		want := u.sent
		if how != "as sent" {
			hidden := regexp.MustCompile(`filename="[^"]*"`).ReplaceAll(u.sent, []byte(`filename="REDACTED"`))
			if bytes.Equal(hidden, u.sent) {
				return failf("the upload holds no file name to hide")
			}
			want = hidden
		}
		return w.lineBody("hotline.content.request.body", string(want))
	})
	sc.Step(`^the line's request body is "([^"]*)"$`, func(written string) error {
		return w.lineBody("hotline.content.request.body", strings.ReplaceAll(written, "~", "\r\n"))
	})
	sc.Step(`^no line holds "([^"]*)"$`, func(text string) error {
		lines, err := w.allFileLines(0)
		if err != nil {
			return err
		}
		for _, line := range lines {
			attrs, _ := line["attributes"].(map[string]any)
			for key, value := range attrs {
				written := sprint(value)
				if strings.HasSuffix(key, ".body") {
					decoded, _ := base64.StdEncoding.DecodeString(written)
					written = string(decoded)
				}
				if strings.Contains(written, text) {
					return failf("the line's %s holds %q", key, text)
				}
			}
		}
		return nil
	})
	sc.Step(`^a byte of the line's request body is changed$`, func() error {
		path := filepath.Join(w.files[0].path, "calls.jsonl")
		data, err := os.ReadFile(path) //nolint:gosec // the scenario's own folder
		if err != nil {
			return err
		}
		key := []byte(`"hotline.content.request.body":"`)
		at := bytes.Index(data, key)
		if at < 0 {
			return failf("calls.jsonl keeps no request body")
		}
		// The first base64 letter turns into another, so the body decodes
		// to other bytes.
		at += len(key)
		data[at] = map[bool]byte{true: 'B', false: 'A'}[data[at] == 'A']
		return os.WriteFile(path, data, 0o600) //nolint:gosec // the scenario's own folder
	})
	sc.Step(`^the line's request body holds "([^"]*)"$`, func(text string) error {
		attrs, err := w.lineAttrs()
		if err != nil {
			return err
		}
		body, err := base64.StdEncoding.DecodeString(sprint(attrs["hotline.content.request.body"]))
		if err != nil || !bytes.Contains(body, []byte(text)) {
			return failf("the line's request body does not hold %q", text)
		}
		return nil
	})
}

// upload sends a multipart body to OpenAI or Stripe, closed, the way an SDK
// sends it, and keeps it to compare with what the vendor got.
func (w *world) upload(u *upload, vendor, path string, body *multipartBody) error {
	body.close()
	u.sent = body.buf.Bytes()
	target, login := "https://api.openai.com"+path, openAILogin
	if vendor == "Stripe" {
		target, login = "https://files.stripe.com"+path, stripeLogin
	}
	return w.post(target, body.w.FormDataContentType(), string(u.sent), login)
}

// keepInFull is the integration's lines for content full, cut at limit
// bytes when one is given, with a file exporter writing to calls.
func (w *world) keepInFull(limit string) string {
	w.files = append(w.files, fileExporter{path: filepath.Join(w.dir, "calls")})
	extra := "    content: full\n"
	if limit != "" {
		extra += "    maxBodySize: " + limit + "\n"
	}
	return extra
}

// markFileShown copies the OpenAI audio spec into dir with its audio file
// marked x-hotline-redact false, and returns the copy's path.
func markFileShown(dir string) (string, error) {
	data, err := os.ReadFile(openAIAudioSpec)
	if err != nil {
		return "", err
	}
	from := "      contentMediaType: application/octet-stream\n"
	marked := strings.Replace(string(data), from, from+"      x-hotline-redact: false\n", 1)
	if marked == string(data) {
		return "", failf("the OpenAI audio spec no longer has the line to mark")
	}
	path := filepath.Join(dir, "openai-audio-shown.openapi.yaml")
	return path, os.WriteFile(path, []byte(marked), 0o600) //nolint:gosec // the scenario's own folder
}

// multipartBody is a multipart/form-data body, written as an app's SDK
// writes it.
type multipartBody struct {
	buf    *bytes.Buffer
	w      *multipart.Writer
	closed bool
}

func newMultipart() *multipartBody {
	buf := &bytes.Buffer{}
	return &multipartBody{buf: buf, w: multipart.NewWriter(buf)}
}

// file adds a file part of size bytes, made up but always the same.
func (b *multipartBody) file(name, fileName, fileType string, size int) {
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, name, fileName))
	if fileType != "" {
		h.Set("Content-Type", fileType)
	}
	part, _ := b.w.CreatePart(h)
	chunk := make([]byte, 4096)
	for i := range chunk {
		chunk[i] = byte(i * 7)
	}
	for size > 0 {
		n := min(size, len(chunk))
		_, _ = part.Write(chunk[:n])
		size -= n
	}
}

// texts adds a text part for each name and value of parts, written as a
// query: model=whisper-1&language=en, in that order.
func (b *multipartBody) texts(parts string) error {
	for pair := range strings.SplitSeq(parts, "&") {
		name, value, _ := strings.Cut(pair, "=")
		name, err := url.QueryUnescape(name)
		if err != nil {
			return err
		}
		b.text(name, value)
	}
	return nil
}

func (b *multipartBody) text(name, value string) {
	_ = b.w.WriteField(name, value)
}

// close writes the closing boundary, once.
func (b *multipartBody) close() {
	if !b.closed {
		b.closed = true
		_ = b.w.Close()
	}
}
