package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (w *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func TestTraceWriterPreservesStreamingDeadlineControl(t *testing.T) {
	w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder(), deadline: time.Now()}
	c, _ := gin.CreateTestContext(w)
	wrapped := &traceWriter{ResponseWriter: c.Writer}
	if err := http.NewResponseController(wrapped).SetWriteDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if !w.deadline.IsZero() {
		t.Fatal("trace logging retained absolute stream deadline")
	}
}
