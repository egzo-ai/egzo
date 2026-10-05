package control

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"time"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

func mustRequest(method, path string, body io.Reader) *http.Request {
	return httptest.NewRequest(method, path, body)
}

// memoryAppend adds an event to memory only: for building a long history without writing it.
func (s *store) memoryAppend(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	event.Seq = s.lastSeq + 1
	event.Time = time.Now().UTC()
	s.add(event)
}
