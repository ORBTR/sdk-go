package orbtr

// Fixture loading and a transport answering from fixtures while recording what the client sent. The
// fixtures in fixtures/ are HTTP exchanges recorded from api.orbtr.ai's handlers; each names the handler it
// came from.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type fixtureFile struct {
	Name         string `json:"name"`
	RecordedFrom string `json:"recorded_from"`
	Request      struct {
		Method string          `json:"method"`
		Path   string          `json:"path"`
		Query  string          `json:"query"`
		Body   json.RawMessage `json:"body"`
	} `json:"request"`
	Response struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"response"`
}

func loadFixture(t testing.TB, name string) fixtureFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("fixtures", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var f fixtureFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func fixtureNames(t testing.TB) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("fixtures", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range paths {
		names = append(names, strings.TrimSuffix(filepath.Base(p), ".json"))
	}
	sort.Strings(names)
	return names
}

type sentRequest struct {
	method string
	path   string
	query  string
	header http.Header
	body   []byte
}

// chunkedReader returns at most n bytes per Read, so stream parsing meets events cut across reads.
type chunkedReader struct {
	data []byte
	n    int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	k := r.n
	if k > len(p) {
		k = len(p)
	}
	if k > len(r.data) {
		k = len(r.data)
	}
	copy(p, r.data[:k])
	r.data = r.data[k:]
	return k, nil
}

type fixtureTransport struct {
	t     testing.TB
	queue []fixtureFile
	sent  []sentRequest
}

func (f *fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	f.sent = append(f.sent, sentRequest{req.Method, req.URL.Path, req.URL.RawQuery, req.Header.Clone(), body})
	if len(f.queue) == 0 {
		f.t.Fatalf("no fixture left for %s %s", req.Method, req.URL.Path)
	}
	fx := f.queue[0]
	f.queue = f.queue[1:]
	h := http.Header{}
	for k, v := range fx.Response.Headers {
		h.Set(k, v)
	}
	return &http.Response{
		StatusCode: fx.Response.Status,
		Header:     h,
		Body:       io.NopCloser(&chunkedReader{data: []byte(fx.Response.Body), n: 7}),
		Request:    req,
	}, nil
}

func fixtureClient(t *testing.T, retries int, names ...string) (*Client, *fixtureTransport) {
	t.Helper()
	tr := &fixtureTransport{t: t}
	for _, n := range names {
		tr.queue = append(tr.queue, loadFixture(t, n))
	}
	c, err := NewClient(WithAPIKey("test-key"), WithBaseURL("https://api.orbtr.test"), WithMaxRetries(retries),
		WithHTTPClient(&http.Client{Transport: tr}))
	if err != nil {
		t.Fatal(err)
	}
	return c, tr
}

func jsonEqual(a, b []byte) bool {
	if len(bytes.TrimSpace(a)) == 0 || len(bytes.TrimSpace(b)) == 0 {
		return len(bytes.TrimSpace(a)) == len(bytes.TrimSpace(b))
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func assertSent(t *testing.T, s sentRequest, name string) {
	t.Helper()
	fx := loadFixture(t, name)
	if s.method != fx.Request.Method || s.path != fx.Request.Path || s.query != fx.Request.Query {
		t.Errorf("%s: sent %s %s?%s, recorded %s %s?%s", name, s.method, s.path, s.query, fx.Request.Method, fx.Request.Path, fx.Request.Query)
	}
	if !jsonEqual(s.body, fx.Request.Body) {
		t.Errorf("%s: sent body %s, recorded %s", name, s.body, fx.Request.Body)
	}
	if got := s.header.Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("%s: Authorization %q", name, got)
	}
	if s.header.Get("X-Api-Key") != "" {
		t.Errorf("%s: sent an x-api-key header", name)
	}
	if !strings.HasPrefix(s.header.Get("User-Agent"), "orbtr-sdk-go/") {
		t.Errorf("%s: User-Agent %q", name, s.header.Get("User-Agent"))
	}
	if len(fx.Request.Body) > 0 && s.header.Get("Content-Type") != "application/json" {
		t.Errorf("%s: Content-Type %q", name, s.header.Get("Content-Type"))
	}
}
