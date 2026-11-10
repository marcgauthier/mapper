package app

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/DataDog/zstd"
)

const zstdLevel = 3
const zstdWebSocketProtocol = "mapper.zstd.v1"
const maxHTTPDecodedBody int64 = 128 << 20
const maxWSDecodedBody int64 = 1 << 20

func acceptsZstd(header string) bool {
	explicit, wildcard := false, 0.0
	quality := 0.0
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		name := strings.ToLower(strings.TrimSpace(fields[0]))
		q := 1.0
		for _, param := range fields[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if ok && strings.EqualFold(key, "q") {
				n, err := strconv.ParseFloat(value, 64)
				if err != nil || n < 0 || n > 1 {
					q = 0
				} else {
					q = n
				}
			}
		}
		if name == "zstd" {
			explicit = true
			if q > quality {
				quality = q
			}
		} else if name == "*" && q > wildcard {
			wildcard = q
		}
	}
	if explicit {
		return quality > 0
	}
	return wildcard > 0
}

func addVary(header http.Header, value string) {
	for _, line := range header.Values("Vary") {
		for _, token := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(token), value) || strings.TrimSpace(token) == "*" {
				return
			}
		}
	}
	header.Add("Vary", value)
}

// All entity bodies use Zstandard level 3. Clients must advertise support;
// handshakes, informational responses, HEAD and bodyless status codes stay HTTP.
func zstdHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrade := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
		addVary(w.Header(), "Accept-Encoding")
		if !upgrade && !acceptsZstd(r.Header.Get("Accept-Encoding")) {
			w.Header().Set("Accept-Encoding", "zstd")
			w.WriteHeader(http.StatusNotAcceptable)
			return
		}
		out := &zstdResponseWriter{ResponseWriter: w, head: r.Method == http.MethodHead}
		defer out.close()
		hasBody := r.ContentLength != 0 || len(r.TransferEncoding) > 0
		encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
		if hasBody || encoding != "" {
			if encoding != "zstd" {
				out.Header().Set("Accept-Encoding", "zstd")
				http.Error(out, "request bodies require Content-Encoding: zstd", http.StatusUnsupportedMediaType)
				return
			}
			limit := maxHTTPDecodedBody
			if r.URL.Path == "/restore" {
				limit = maxBackupUpload
			}
			original := r.Body
			defer original.Close()
			reader := zstd.NewReader(http.MaxBytesReader(out, original, limit+limit/64+(1<<20)))
			defer reader.Close()
			body, size, cleanup, err := spoolDecodedBody(reader, limit)
			if cleanup != nil {
				defer cleanup()
			}
			if err != nil {
				var maxErr *http.MaxBytesError
				if errors.As(err, &maxErr) {
					http.Error(out, "request body exceeds limit", 413)
				} else {
					http.Error(out, "invalid Zstandard request body", 400)
				}
				return
			}
			r.Body = body
			r.ContentLength = size
			r.TransferEncoding = nil
			r.Header.Del("Content-Encoding")
			r.Header.Del("Content-Length")
		}
		r.Header.Del("Range")
		r.Header.Del("If-Range")
		next.ServeHTTP(out, r)
	})
}

// Verify decoding before handing a mutation to the app. Large uploads spill to
// a private temporary file, avoiding a backup-sized allocation in server memory.
func spoolDecodedBody(source io.Reader, limit int64) (io.ReadCloser, int64, func(), error) {
	var memory bytes.Buffer
	initial := int64(1 << 20)
	if limit < initial {
		initial = limit + 1
	}
	first, err := io.CopyN(&memory, source, initial)
	if first > limit {
		return nil, 0, nil, &http.MaxBytesError{Limit: limit}
	}
	if err != nil {
		if err == io.EOF {
			return io.NopCloser(bytes.NewReader(memory.Bytes())), first, func() {}, nil
		}
		return nil, 0, nil, err
	}
	file, err := os.CreateTemp("", "mapper-zstd-upload-*")
	if err != nil {
		return nil, 0, nil, err
	}
	cleanup := func() { file.Close(); os.Remove(file.Name()) }
	if _, err = file.Write(memory.Bytes()); err != nil {
		return nil, 0, cleanup, err
	}
	count, err := io.Copy(file, io.LimitReader(source, limit-first+1))
	size := first + count
	if size > limit {
		return nil, 0, cleanup, &http.MaxBytesError{Limit: limit}
	}
	if err != nil {
		return nil, 0, cleanup, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, 0, cleanup, err
	}
	return file, size, cleanup, nil
}

type zstdResponseWriter struct {
	http.ResponseWriter
	writer *zstd.Writer
	status int
	head   bool
}

func (w *zstdResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *zstdResponseWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = status
	if status != 204 && status != 304 {
		w.Header().Del("Content-Length")
		w.Header().Set("Content-Encoding", "zstd")
		w.Header().Set("X-Mapper-Compression-Level", "3")
		if !w.head {
			w.writer = zstd.NewWriterLevel(w.ResponseWriter, zstdLevel)
		}
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *zstdResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType(data))
		}
		w.WriteHeader(200)
	}
	if w.head {
		return len(data), nil
	}
	if w.writer == nil {
		return 0, http.ErrBodyNotAllowed
	}
	total := 0
	for len(data) > 0 {
		n := len(data)
		if n > 32<<10 {
			n = 32 << 10
		}
		written, err := w.writer.Write(data[:n])
		total += written
		if err != nil {
			return total, err
		}
		data = data[written:]
		if written == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}
func (w *zstdResponseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	if w.writer != nil {
		_ = w.writer.Flush()
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *zstdResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}
func (w *zstdResponseWriter) ReadFrom(r io.Reader) (int64, error) {
	return io.CopyBuffer(struct{ io.Writer }{w}, r, make([]byte, 32<<10))
}
func (w *zstdResponseWriter) close() {
	if w.writer != nil {
		_ = w.writer.Close()
	}
}

func zstdMessage(data []byte) ([]byte, error) { return zstd.CompressLevel(nil, data, zstdLevel) }

var wsDecodeBuffers = sync.Pool{New: func() any { return make([]byte, maxWSDecodedBody) }}

func decodeZstdMessage(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty WebSocket frame")
	}
	buffer := wsDecodeBuffers.Get().([]byte)
	defer wsDecodeBuffers.Put(buffer)
	n, err := zstd.DecompressInto(buffer, data)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), buffer[:n]...), nil
}
