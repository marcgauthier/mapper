package app

import (
	"bytes"
	"github.com/DataDog/zstd"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestZstdHTTPRoundTrip(t *testing.T) {
	plain := bytes.Repeat([]byte("mapper-payload"), 200000)
	encoded, err := zstd.CompressLevel(nil, plain, 3)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	handler := zstdHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		b, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(b, plain) {
			t.Fatal("decoded upload differs", err)
		}
		if r.Header.Get("Content-Encoding") != "" {
			t.Fatal("encoding retained")
		}
		w.Write(b)
	}))
	req := httptest.NewRequest("POST", "/api/test", bytes.NewReader(encoded))
	req.Header.Set("Accept-Encoding", "gzip, zstd")
	req.Header.Set("Content-Encoding", "zstd")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	got, err := zstd.Decompress(nil, w.Body.Bytes())
	if !called || w.Code != 200 || err != nil || !bytes.Equal(got, plain) || w.Header().Get("Content-Encoding") != "zstd" || w.Header().Get("X-Mapper-Compression-Level") != "3" {
		t.Fatalf("round trip: %d %v", w.Code, err)
	}
}
func TestZstdNegotiationAndInvalidUpload(t *testing.T) {
	for _, tc := range []struct {
		accept, encoding, body string
		code                   int
	}{{"", "", "", 406}, {"zstd;q=0, *;q=1", "", "", 406}, {"zstd", "", "raw", 415}, {"zstd", "zstd", "invalid", 400}} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/test", bytes.NewBufferString(tc.body))
		r.Header.Set("Accept-Encoding", tc.accept)
		r.Header.Set("Content-Encoding", tc.encoding)
		zstdHTTP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid request reached handler") })).ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("%+v: %d", tc, w.Code)
		}
	}
}
func TestZstdWebSocketBoundedDecode(t *testing.T) {
	frame, err := zstdMessage([]byte(`{"type":"ready"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeZstdMessage(frame)
	if err != nil || string(got) != `{"type":"ready"}` {
		t.Fatalf("%s %v", got, err)
	}
	large, _ := zstd.CompressLevel(nil, bytes.Repeat([]byte("x"), int(maxWSDecodedBody)+1), 3)
	if _, err = decodeZstdMessage(large); err == nil {
		t.Fatal("accepted oversized message")
	}
}
