package server

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/handlers"
	"github.com/trevoraspencer/droid-proxy/internal/logging"
)

const requestIDHeader = "X-Request-ID"
const requestIDKey = "request_id"

// RequestID assigns a request id from the inbound header or generates one.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.GetHeader(requestIDHeader))
		if id == "" {
			id = randomID()
		}
		c.Set(requestIDKey, id)
		c.Writer.Header().Set(requestIDHeader, id)
		c.Next()
	}
}

func randomID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "x"
	}
	return hex.EncodeToString(buf[:])
}

// AccessLog logs each request at info level with redacted fields.
func AccessLog(logger *logrus.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		fields := logrus.Fields{
			"method":     c.Request.Method,
			"path":       c.Request.URL.Path,
			"status":     c.Writer.Status(),
			"bytes":      c.Writer.Size(),
			"duration":   time.Since(start).String(),
			"request_id": c.GetString(requestIDKey),
		}
		if ua := c.GetHeader("User-Agent"); ua != "" {
			fields["ua"] = logging.Redact(ua)
		}
		logger.WithFields(fields).Info("request")
	}
}

const traceBodyMaxBytes = 4096

type traceWriter struct {
	gin.ResponseWriter
	buf bytes.Buffer
}

func (w *traceWriter) Write(b []byte) (int, error) {
	if w.buf.Len() < traceBodyMaxBytes {
		remain := traceBodyMaxBytes - w.buf.Len()
		if len(b) > remain {
			w.buf.Write(b[:remain])
		} else {
			w.buf.Write(b)
		}
	}
	return w.ResponseWriter.Write(b)
}

func (w *traceWriter) WriteString(s string) (int, error) {
	if w.buf.Len() < traceBodyMaxBytes {
		remain := traceBodyMaxBytes - w.buf.Len()
		if len(s) > remain {
			w.buf.WriteString(s[:remain])
		} else {
			w.buf.WriteString(s)
		}
	}
	return w.ResponseWriter.WriteString(s)
}

// TraceLog emits bounded, redacted request/response samples only when explicitly
// enabled. Default logging never includes bodies or credentials.
func TraceLog(cfg *config.Config, logger *logrus.Logger) gin.HandlerFunc {
	if cfg == nil || !cfg.Logging.TraceRequests {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		tw := &traceWriter{ResponseWriter: c.Writer}
		c.Writer = tw
		c.Next()
		fields := logrus.Fields{
			"request_id": c.GetString(requestIDKey),
			"method":     c.Request.Method,
			"path":       logging.Redact(c.Request.URL.Redacted()),
			"status":     c.Writer.Status(),
		}
		if v, ok := c.Get(handlers.TraceRequestBodyKey); ok {
			if b, ok := v.([]byte); ok {
				fields["request_body"] = traceSample(b, cfg.Logging.Redact)
			}
		}
		if tw.buf.Len() > 0 {
			fields["response_body"] = traceSample(tw.buf.Bytes(), cfg.Logging.Redact)
		}
		logger.WithFields(fields).Debug("http trace")
	}
}

func traceSample(b []byte, redact bool) string {
	if len(b) > traceBodyMaxBytes {
		b = b[:traceBodyMaxBytes]
	}
	s := string(b)
	if redact {
		s = logging.Redact(s)
	}
	return s
}

// Recovery turns panics into 500 errors and logs them.
func Recovery(logger *logrus.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.WithField("panic", logging.Redact(strings.TrimSpace(toString(rec)))).Error("panic recovered")
				if !c.Writer.Written() {
					handlers.WriteJSONError(c, http.StatusInternalServerError, "internal_error", "internal server error")
				}
			}
		}()
		c.Next()
	}
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case error:
		return x.Error()
	default:
		return ""
	}
}

// RequestBodyLimit applies the configured finite request body cap after auth
// middleware and before any handler reads/parses/translates the body. A zero
// limit is an explicit opt-out.
func RequestBodyLimit(cfg *config.Config) gin.HandlerFunc {
	limit := cfg.Server.RequestBodyMaxBytes
	if limit <= 0 {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		if c.Request != nil && c.Request.Body != nil && c.Request.ContentLength > limit {
			handlers.WritePayloadTooLarge(c)
			c.Abort()
			return
		}
		if c.Request != nil && c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		}
		c.Next()
	}
}

// ClientAuth enforces the configured client auth scheme. When ClientAuth.Enabled is false
// it is a no-op.
func ClientAuth(cfg *config.Config) gin.HandlerFunc {
	if !cfg.ClientAuth.Enabled {
		return func(c *gin.Context) { c.Next() }
	}
	header := cfg.ClientAuth.Header
	if header == "" {
		header = "Authorization"
	}
	scheme := cfg.ClientAuth.Scheme
	keys := make([][sha256.Size]byte, 0, len(cfg.ClientAuth.APIKeys))
	for _, k := range cfg.ClientAuth.APIKeys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		keys = append(keys, sha256.Sum256([]byte(k)))
	}
	return func(c *gin.Context) {
		raw := strings.TrimSpace(c.GetHeader(header))
		if raw == "" {
			handlers.WriteJSONError(c, http.StatusUnauthorized, "authentication_error", "missing "+header+" header")
			c.Abort()
			return
		}
		got := raw
		if scheme != "" {
			prefix := scheme + " "
			if !strings.HasPrefix(raw, prefix) {
				handlers.WriteJSONError(c, http.StatusUnauthorized, "authentication_error", "expected scheme "+scheme)
				c.Abort()
				return
			}
			got = strings.TrimSpace(strings.TrimPrefix(raw, prefix))
		}
		if !clientAPIKeyMatches(got, keys) {
			handlers.WriteJSONError(c, http.StatusUnauthorized, "authentication_error", "invalid api key")
			c.Abort()
			return
		}
		c.Next()
	}
}

func clientAPIKeyMatches(got string, keys [][sha256.Size]byte) bool {
	gotHash := sha256.Sum256([]byte(got))
	match := 0
	for i := range keys {
		match |= subtle.ConstantTimeCompare(gotHash[:], keys[i][:])
	}
	return match == 1
}
