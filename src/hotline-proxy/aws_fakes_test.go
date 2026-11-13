package proxy_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // SQS's own message checksum
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// fakeSQS answers SendMessage, ReceiveMessage and DeleteMessage the way SQS
// does over its JSON protocol, keeping each queue's messages apart.
type fakeSQS struct {
	mu       sync.Mutex
	calls    []*http.Request
	messages []sqsMessage
	next     int
}

type sqsMessage struct {
	queue, id, body, receipt string
	inFlight                 bool
}

func (f *fakeSQS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	_ = json.NewDecoder(r.Body).Decode(&in)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Clone(context.Background()))
	var out any
	switch r.Header.Get("X-Amz-Target") {
	case "AmazonSQS.SendMessage":
		queue, _ := in["QueueUrl"].(string)
		body, _ := in["MessageBody"].(string)
		m := f.add(queue, body)
		out = map[string]any{"MessageId": m.id, "MD5OfMessageBody": md5Hex(m.body)}
	case "AmazonSQS.ReceiveMessage":
		var got []map[string]any
		for i := range f.messages {
			m := &f.messages[i]
			if !m.inFlight && m.queue == in["QueueUrl"] {
				m.inFlight = true
				got = append(got, map[string]any{"MessageId": m.id, "ReceiptHandle": m.receipt, "MD5OfBody": md5Hex(m.body), "Body": m.body})
			}
		}
		// Like SQS, an empty receive has no Messages at all.
		reply := map[string]any{}
		if len(got) > 0 {
			reply["Messages"] = got
		}
		out = reply
	case "AmazonSQS.DeleteMessage":
		f.messages = slices.DeleteFunc(f.messages, func(m sqsMessage) bool { return m.receipt == in["ReceiptHandle"] })
		out = map[string]any{}
	default:
		w.WriteHeader(http.StatusBadRequest)
		out = map[string]any{"__type": "com.amazonaws.sqs#InvalidAction", "message": "unknown action"}
	}
	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		panic(err)
	}
}

// add puts a message on a queue; the caller holds the lock.
func (f *fakeSQS) add(queue, body string) sqsMessage {
	f.next++
	m := sqsMessage{queue: queue, id: fmt.Sprintf("m-%d", f.next), body: body, receipt: fmt.Sprintf("r-%d", f.next)}
	f.messages = append(f.messages, m)
	return m
}

// put puts a message on a queue, as another AWS service would.
func (f *fakeSQS) put(queue, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.add(queue, body)
}

func (f *fakeSQS) waiting() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.messages)
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // SQS's own message checksum
	return hex.EncodeToString(sum[:])
}

// exampleApp builds the example program once per run.
type exampleApp struct {
	dir    string
	once   sync.Once
	binary string
	err    error
}

func (e *exampleApp) build() (string, error) {
	e.once.Do(func() {
		e.binary = filepath.Join(e.dir, "aws-shop")
		build := exec.CommandContext(context.Background(), "go", "build", "-o", e.binary, "hotline-example/aws-shop") //nolint:gosec // builds the example app
		if out, err := build.CombinedOutput(); err != nil {
			e.err = fmt.Errorf("%w: building the example app: %s", err, out)
		}
	})
	return e.binary, e.err
}

// fakeS3 answers PutObject, GetObject, ListObjectsV2 and DeleteObjects for
// one bucket, the way S3 does, with the bucket in the host name.
type fakeS3 struct {
	bucket string
	mu     sync.Mutex
	calls  []s3Call
	files  map[string]s3File
}

type s3Call struct {
	method, uri       string
	tls               bool
	authorization     string
	proxyAuth         string
	chunked, checksum bool
}

type s3File struct {
	data     []byte
	modified time.Time
}

func newFakeS3(bucket string) *fakeS3 {
	return &fakeS3{bucket: bucket, files: map[string]s3File{}}
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	call := s3Call{
		method: r.Method, uri: r.RequestURI, tls: r.TLS != nil,
		authorization: r.Header.Get("Authorization"), proxyAuth: r.Header.Get("Proxy-Authorization"),
	}
	if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
		call.chunked = true
		var trailer map[string]string
		data, trailer = unchunk(data)
		sum := make([]byte, 4)
		binary.BigEndian.PutUint32(sum, crc32.ChecksumIEEE(data))
		call.checksum = trailer["x-amz-checksum-crc32"] == base64.StdEncoding.EncodeToString(sum)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	key := strings.TrimPrefix(r.URL.Path, "/")
	query := r.URL.Query()
	switch {
	case r.Method == http.MethodGet && key == "" && query.Get("list-type") == "2":
		f.list(w)
	case r.Method == http.MethodPost && key == "" && query.Has("delete"):
		f.delete(w, data)
	case r.Method == http.MethodPut && key != "":
		f.files[key] = s3File{data: data, modified: time.Now().UTC()}
		w.Header().Set("ETag", `"e-1"`)
	case r.Method == http.MethodGet && key != "":
		file, ok := f.files[key]
		if !ok {
			s3XML(w, http.StatusNotFound, `<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message><RequestId>req-1</RequestId></Error>`)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(file.data)
	default:
		s3XML(w, http.StatusBadRequest, `<Error><Code>InvalidRequest</Code><Message>not in the fake</Message></Error>`)
	}
}

func (f *fakeS3) list(w http.ResponseWriter) {
	keys := make([]string, 0, len(f.files))
	for k := range f.files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>%s</Name><Prefix></Prefix><KeyCount>%d</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>`, f.bucket, len(keys))
	for _, k := range keys {
		file := f.files[k]
		fmt.Fprintf(&b, `<Contents><Key>%s</Key><LastModified>%s</LastModified><ETag>"e-1"</ETag><Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>`,
			k, file.modified.Format("2006-01-02T15:04:05.000Z"), len(file.data))
	}
	b.WriteString(`</ListBucketResult>`)
	s3XML(w, http.StatusOK, b.String())
}

func (f *fakeS3) delete(w http.ResponseWriter, data []byte) {
	var in struct {
		Objects []struct{ Key string } `xml:"Object"`
	}
	if err := xml.Unmarshal(data, &in); err != nil {
		s3XML(w, http.StatusBadRequest, `<Error><Code>MalformedXML</Code></Error>`)
		return
	}
	var b strings.Builder
	b.WriteString(`<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	for _, o := range in.Objects {
		delete(f.files, o.Key)
		fmt.Fprintf(&b, `<Deleted><Key>%s</Key></Deleted>`, o.Key)
	}
	b.WriteString(`</DeleteResult>`)
	s3XML(w, http.StatusOK, b.String())
}

func (f *fakeS3) put(key string, data []byte, modified time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[key] = s3File{data: data, modified: modified}
}

func (f *fakeS3) file(key string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files[key].data
}

func (f *fakeS3) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.files)
}

func (f *fakeS3) received() []s3Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func s3XML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("x-amz-request-id", "req-1")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+body)
}

// unchunk reads AWS's chunked body: each chunk is its size in hex, a line
// break, the bytes and a line break; a chunk of size 0 ends it, followed
// by trailer lines such as the checksum.
func unchunk(data []byte) ([]byte, map[string]string) {
	in := bufio.NewReader(bytes.NewReader(data))
	var body []byte
	for {
		line, err := in.ReadString('\n')
		sizeText, _, _ := strings.Cut(strings.TrimSpace(line), ";")
		size, perr := strconv.ParseInt(sizeText, 16, 64)
		if err != nil || perr != nil || size == 0 {
			break
		}
		chunk := make([]byte, size+2)
		if _, err := io.ReadFull(in, chunk); err != nil {
			break
		}
		body = append(body, chunk[:size]...)
	}
	trailer := map[string]string{}
	rest, _ := io.ReadAll(in)
	for _, line := range strings.Split(string(rest), "\n") {
		if name, value, found := strings.Cut(strings.TrimSpace(line), ":"); found {
			trailer[strings.ToLower(name)] = value
		}
	}
	return body, trailer
}

// shopConfig is testdata/aws/hotline-proxy.yaml with the given changes,
// each to text the file holds once, its admin port free, and its specs,
// overlays and key read from where they are in the repo.
func shopConfig(changes ...[2]string) (string, error) {
	dir, err := filepath.Abs(filepath.Join("testdata", "aws"))
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, "hotline-proxy.yaml")) //nolint:gosec // the test's own file
	if err != nil {
		return "", err
	}
	config := string(data)
	changes = append(changes,
		[2]string{"tcp://:8081", "tcp://127.0.0.1:0"},
	)
	for _, change := range changes {
		if strings.Count(config, change[0]) != 1 {
			return "", failf("the example config no longer has %s once", change[0])
		}
		config = strings.Replace(config, change[0], change[1], 1)
	}
	config = strings.ReplaceAll(config, "[./", "["+dir+"/")
	config = strings.ReplaceAll(config, "file: ./", "file: "+dir+"/")
	return strings.ReplaceAll(config, "../../../../specs/", filepath.Join(dir, "..", "..", "..", "..", "specs")+"/"), nil
}
