package dockerapi

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"strings"
)

// The error sentinels and carriers live in dockerclient so that every
// implementation reports the same values; see types.go for the aliases. What
// remains here is the one piece that is specific to speaking HTTP: turning a
// daemon response into a StatusError.

// errorResponse is the daemon's JSON error envelope.
type errorResponse struct {
	Message string `json:"message"`
}

// maxErrorBody bounds how much of an error response is read. A daemon that
// answers a request with an HTML page or a stream should not be able to pull
// an unbounded amount into an error message.
const maxErrorBody = 1 << 20

// newStatusError reads the response body and builds a StatusError. The body
// is consumed; the caller still closes it.
func newStatusError(resp *http.Response, method, path string) *StatusError {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	msg := strings.TrimSpace(string(b))
	var body errorResponse
	if json.Unmarshal(b, &body) == nil && body.Message != "" {
		msg = body.Message
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	if msg == "" {
		// StatusText is empty for codes outside the registered set, which
		// would otherwise leave the error trailing off after the colon.
		if msg = http.StatusText(resp.StatusCode); msg == "" {
			msg = "the daemon gave no message"
		}
	}
	return &StatusError{StatusCode: resp.StatusCode, Message: msg, Method: method, Path: path}
}
