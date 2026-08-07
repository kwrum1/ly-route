package httpapi

import (
	"errors"
	"net/http"
	"testing"
)

type disconnectingResponseWriter struct {
	header http.Header
}

func (writer *disconnectingResponseWriter) Header() http.Header {
	if writer.header == nil {
		writer.header = http.Header{}
	}
	return writer.header
}

func (*disconnectingResponseWriter) WriteHeader(int) {}

func (*disconnectingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("client disconnected")
}

func TestWriteJSONDoesNotPanicWhenClientDisconnects(t *testing.T) {
	writer := &disconnectingResponseWriter{}
	writeJSON(writer, http.StatusOK, map[string]any{"items": []string{"large response"}})
}
