package dockerapi

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"wslc-bridge/internal/native"
)

type orderedResponseWriter struct {
	header http.Header
	body   bytes.Buffer
	events *[]string
}

func (w *orderedResponseWriter) Header() http.Header { return w.header }

func (w *orderedResponseWriter) WriteHeader(status int) {
	*w.events = append(*w.events, "status")
}

func (w *orderedResponseWriter) Write(body []byte) (int, error) {
	*w.events = append(*w.events, "write")
	return w.body.Write(body)
}

func (w *orderedResponseWriter) Flush() {
	*w.events = append(*w.events, "flush")
}

type orderedBody struct {
	readOnce bool
	events   *[]string
}

func (b *orderedBody) Read(target []byte) (int, error) {
	*b.events = append(*b.events, "read")
	if b.readOnce {
		return 0, io.EOF
	}
	b.readOnce = true
	return copy(target, []byte("event")), nil
}

func (b *orderedBody) Close() error { return nil }

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestPingIsProxiedOverGuestTransport(t *testing.T) {
	guest := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/_ping" || request.Method != http.MethodGet {
			t.Fatalf("unexpected guest request: %s %s", request.Method, request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/plain"}}, Body: io.NopCloser(strings.NewReader("OK"))}, nil
	})}
	handler := NewHandler(native.NewClient("", ""), guest)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/_ping", nil))
	if response.Code != http.StatusOK || response.Body.String() != "OK" {
		t.Fatalf("unexpected ping response: status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestVersionedPingPreservesSupportedAPIPrefixForGuest(t *testing.T) {
	guest := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1.44/_ping" {
			t.Fatalf("version prefix was not forwarded: %s", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("OK"))}, nil
	})}
	handler := NewHandler(native.NewClient("", ""), guest)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1.44/_ping", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("versioned ping returned %d", response.Code)
	}
}

func TestUnsupportedAPIVersionDoesNotReachGuest(t *testing.T) {
	guest := &http.Client{Transport: roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		t.Fatal("unsupported API version reached guest")
		return nil, nil
	})}
	handler := NewHandler(native.NewClient("", ""), guest)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1.23/_ping", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported version returned %d, want 400", response.Code)
	}
}

func TestStreamingReadFlushesHeadersBeforeWaitingForFirstPayload(t *testing.T) {
	events := make([]string, 0, 8)
	guest := &http.Client{Transport: roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       &orderedBody{events: &events},
		}, nil
	})}
	handler := NewHandler(native.NewClient("", ""), guest)
	writer := &orderedResponseWriter{header: make(http.Header), events: &events}
	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/events", nil))

	flushIndex := -1
	readIndex := -1
	for index, event := range events {
		if event == "flush" && flushIndex == -1 {
			flushIndex = index
		}
		if event == "read" {
			readIndex = index
			break
		}
	}
	if flushIndex == -1 || readIndex == -1 || flushIndex > readIndex {
		t.Fatalf("stream response did not flush headers before reading first payload: %v", events)
	}
}
