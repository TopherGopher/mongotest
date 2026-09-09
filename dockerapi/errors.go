package dockerapi

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
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

// maxErrorMessage bounds the daemon message carried on a StatusError, in
// bytes. Long enough for any real message, short enough that a stray HTML
// page does not fill a log line.
const maxErrorMessage = 200

// ellipsis marks a message that was cut, so a reader can tell the difference
// between a truncated message and a daemon that stopped mid-sentence.
const ellipsis = "…"

// truncateMessage makes a daemon message safe to carry on an error: valid
// UTF-8, and at most limit bytes with the ellipsis included.
//
// Both halves matter. The body is whatever answered the socket, so it can
// already contain invalid bytes, and cutting valid UTF-8 at an arbitrary
// byte offset creates them. Either way the result travels into logs and into
// anything that re-encodes the error as JSON.
func truncateMessage(msg string, limit int) string {
	if !utf8.ValidString(msg) {
		msg = strings.ToValidUTF8(msg, "\uFFFD")
	}
	if len(msg) <= limit {
		return msg
	}
	cut := limit - len(ellipsis)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return strings.TrimRight(msg[:cut], " \t\n") + ellipsis
}

// newStatusError reads the response body and builds a StatusError. The body
// is consumed; the caller still closes it.
func newStatusError(resp *http.Response, method, path string) *StatusError {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	msg := strings.TrimSpace(string(b))
	var body errorResponse
	if json.Unmarshal(b, &body) == nil && body.Message != "" {
		msg = body.Message
	}
	msg = truncateMessage(msg, maxErrorMessage)
	if msg == "" {
		// StatusText is empty for codes outside the registered set, which
		// would otherwise leave the error trailing off after the colon.
		if msg = http.StatusText(resp.StatusCode); msg == "" {
			msg = "the daemon gave no message"
		}
	}
	return &StatusError{StatusCode: resp.StatusCode, Message: msg, Method: method, Path: path}
}
