package mobyclient

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"

	"github.com/tophergopher/mongotest/dockerclient"
)

// daemonErrorPrefix is what the moby client's checkResponseErr prepends to
// every daemon-reported message ("Error response from daemon: <message>").
// Trimming it leaves the same bare message dockerapi reports, so a caller
// sees identical text whichever client produced the error.
const daemonErrorPrefix = "Error response from daemon: "

// execUpgradeFailed matches the plain error the moby client returns when an
// exec start does not get the 101 upgrade it asked for. Unlike every other
// failure path, this one is not classified with containerd/errdefs, because
// the moby client builds it before the response is otherwise interpreted;
// the status code is still in the message, so it is recovered here.
var execUpgradeFailed = regexp.MustCompile(`^unable to upgrade to \w+, received (\d+)$`)

// mapError turns an error the moby client returned for one request into the
// shared dockerclient sentinels and carriers, so callers branch with
// errors.Is regardless of which client produced the failure. method and
// path describe the request that failed, for StatusError and ResponseError
// to report; host is the daemon address, for ConnectionError to report.
func mapError(host, method, path string, err error) error {
	if err == nil {
		return nil
	}
	// Context errors are returned as-is so callers can compare them
	// directly, exactly as dockerclient.WrapConnectionError does.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	switch {
	case cerrdefs.IsNotFound(err):
		return statusError(http.StatusNotFound, method, path, err)
	case cerrdefs.IsConflict(err):
		return statusError(http.StatusConflict, method, path, err)
	case cerrdefs.IsUnauthorized(err):
		return statusError(http.StatusUnauthorized, method, path, err)
	case cerrdefs.IsPermissionDenied(err):
		return statusError(http.StatusForbidden, method, path, err)
	case cerrdefs.IsInvalidArgument(err):
		return statusError(http.StatusBadRequest, method, path, err)
	}
	// Not a classified daemon response: it may be that the daemon could not
	// be reached at all. WrapConnectionError inspects the error chain
	// itself (fs.ErrNotExist, a timed-out net.Error, a DNS failure, ...) so
	// this works whichever moby code path produced the error, and returns
	// err unchanged when it does not recognise the cause.
	// WrapConnectionError signals "not a connection problem" by returning
	// its input unchanged, so the test is whether it produced a
	// ConnectionError. It cannot be a == comparison: two interface values
	// of the same uncomparable dynamic type panic at run time, and an
	// error type is free to be a struct carrying a slice.
	wrapped := dockerclient.WrapConnectionError(host, err)
	var connErr *dockerclient.ConnectionError
	if errors.As(wrapped, &connErr) {
		return wrapped
	}
	if m := execUpgradeFailed.FindStringSubmatch(err.Error()); m != nil {
		code, _ := strconv.Atoi(m[1])
		return &dockerclient.StatusError{StatusCode: code, Method: method, Path: path,
			Message: "the daemon did not upgrade the exec stream to a raw connection"}
	}
	return &dockerclient.ResponseError{Method: method, Path: path, Err: err,
		Problem: "the docker daemon returned an error mobyclient could not classify"}
}

// statusError builds a dockerclient.StatusError for a classified daemon
// response, with the daemon's own message and no "Error response from
// daemon:" prefix.
func statusError(code int, method, path string, err error) *dockerclient.StatusError {
	return &dockerclient.StatusError{StatusCode: code, Method: method, Path: path, Message: daemonMessage(err)}
}

// daemonMessage strips the moby client's own wrapping prefix, leaving the
// daemon's message exactly as dockerapi reports it.
func daemonMessage(err error) string {
	return strings.TrimPrefix(err.Error(), daemonErrorPrefix)
}
