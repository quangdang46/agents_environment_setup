package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// retryDelays is the fixed retry schedule for transient download failures,
// mirroring acfs_curl_with_retry's (0 5 15): the first attempt runs
// immediately, then waits of 5 and 15 seconds. Fixed rather than
// exponential because the schedule is a property of the remote — a CDN or a
// rate limiter recovering — not of the retry count. A var so tests can
// substitute a zero schedule without waiting.
//
// Retry never touches a privilege path by construction: the retried unit is
// http.Client.Do plus a body copy. There is no sudo, no Authorize, no
// exec.RunAuthorized in this file. A reader tempted to "extend" retry to
// apt-get update or brew install must not: a second attempt there is how a
// tool ends up waiting on a prompt that nobody is there to answer (see the
// NO-RETRY rationale on PackageInstaller).
var retryDelays = []time.Duration{0, 5 * time.Second, 15 * time.Second}

// sleepFn suspends a backoff wait. A var so tests record the delays without
// sleeping, and so cancellation is honoured through ctx in production.
var sleepFn = func(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// maxRetryAfter caps an upstream Retry-After header: a CDN may ask for
// minutes, and a run must not sleep longer than the schedule's budget
// already accounts for.
const maxRetryAfter = 300 * time.Second

// statusError carries the HTTP status of a completed response plus any
// Retry-After it served, which the retry classifier needs and a plain error
// loses. It exists only inside the retry decision: the text a user sees is
// produced from its fields and asserted on by existing tests.
type statusError struct {
	code   int
	status string
	// after is the parsed Retry-After, 0 when absent or unparsable.
	after time.Duration
}

func (e *statusError) Error() string { return "unexpected status " + e.status }

// doDownloadWithRetry GETs url and streams the response body into dst, which
// is rewound and truncated before every attempt so a partial first attempt
// cannot contaminate a later one.
//
// An attempt is retried only when it failed transiently; the first fatal
// error is returned immediately, wrapped once with the URL. A nil log
// disables the retry lines, and the log is written before the wait so a
// long backoff explains itself.
func doDownloadWithRetry(ctx context.Context, client *http.Client, url string, dst *os.File, log io.Writer) error {
	var lastErr error
	for i := range retryDelays {
		if i > 0 {
			if err := sleepFn(ctx, retryDelays[i]); err != nil {
				return fmt.Errorf("download %s: %w", url, err)
			}
		}

		status, err := attemptDownload(ctx, client, url, dst)
		if err == nil {
			return nil
		}
		lastErr = err

		retryable, after := classifyDownloadErr(err, status)
		if !retryable || i == len(retryDelays)-1 {
			break
		}

		// Retry-After carried on the status error wins when present: the
		// status alone says "retry", and the header says "not yet".
		if se, ok := lastErr.(*statusError); ok && se.after > 0 {
			after = se.after
		}
		logRetry(log, i+1, len(retryDelays), err, after)
		if err := sleepFn(ctx, after); err != nil {
			return fmt.Errorf("download %s: %w", url, err)
		}
	}
	return fmt.Errorf("download %s: %w", url, lastErr)
}

// attemptDownload issues one request and streams the body to dst. It returns
// the HTTP status (0 when no response arrived) and the error, if any.
func attemptDownload(ctx context.Context, client *http.Client, url string, dst *os.File) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Drain a bounded amount so the connection can be reused, then report
		// the status as an error the classifier can read. The Retry-After
		// header is parsed now, while the response is still held; afterwards
		// the information is gone.
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return resp.StatusCode, &statusError{code: resp.StatusCode, status: resp.Status, after: parseRetryAfter(resp.Header.Get("Retry-After"))}
	}

	if err := rewind(dst); err != nil {
		return 0, err
	}
	// LimitReader's extra byte turns an over-large asset into a clean error
	// rather than a truncated file that would then fail checksum.
	written, err := io.Copy(dst, io.LimitReader(resp.Body, maxAssetBytes+1))
	if err != nil {
		return 0, err
	}
	if written > maxAssetBytes {
		return 0, fmt.Errorf("asset exceeds %d bytes", maxAssetBytes)
	}
	return 0, nil
}

// rewind puts the destination back at a known-empty state for a fresh
// attempt. Truncate alone moves the offset to the new end on some
// platforms, so the seek order matters.
func rewind(f *os.File) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek %s: %w", f.Name(), err)
	}
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("truncate %s: %w", f.Name(), err)
	}
	return nil
}

// classifyDownloadErr decides whether a failed attempt is worth repeating.
// Retryable is the curl set 6|7|28|35|52|56 expressed in Go types — DNS,
// connect, timeout, TLS handshake, empty reply — plus HTTP 429 and 5xx.
//
// Fatal is everything retrying cannot change: 4xx (a 404 is the same 404 in
// fifteen seconds), a cancelled or expired context, and anything that
// arrived intact but wrong. A TLS *verification* failure is fatal on purpose:
// retrying is the wrong reflex when a certificate did not validate, because
// the second answer may be the attacker's.
func classifyDownloadErr(err error, status int) (retryable bool, after time.Duration) {
	def := defaultRetryDelay()
	if err == nil {
		return false, 0
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, 0
	}
	if errors.Is(err, ErrChecksum) {
		return false, 0
	}

	// A completed response is the most specific signal available, so it is
	// consulted before the transport-level errors below.
	if se, ok := err.(*statusError); ok && se.code == status {
		switch {
		case se.code == http.StatusTooManyRequests || (se.code >= 500 && se.code <= 599):
			return true, def
		default:
			return false, 0
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true, def
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true, def
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ETIMEDOUT) {
		return true, def
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true, def
	}

	// Unclassified, therefore fatal. An unknown error repeated three times is
	// three copies of the same wrong answer to show the user.
	return false, 0
}

func defaultRetryDelay() time.Duration {
	if len(retryDelays) > 1 {
		return retryDelays[1]
	}
	return 0
}

// parseRetryAfter reads a Retry-After header as delta-seconds, capped at
// maxRetryAfter. Only the delta-seconds form is honoured: the HTTP-date form
// is legal but a CDN sending it is asking for a client to compare clocks,
// and the schedule's own delay is a better answer than a wrong clock.
// Absent, unparsable and negative values return 0, and the caller falls back
// to the schedule.
func parseRetryAfter(v string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	d := time.Duration(secs) * time.Second
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

// logRetry records one retried attempt. It is deliberately quiet on success:
// the line exists so a slow run explains why it is still going.
func logRetry(log io.Writer, attempt, total int, err error, after time.Duration) {
	if log == nil {
		return
	}
	fmt.Fprintf(log, "    → attempt %d/%d failed (%v), retrying in %s\n", attempt, total, err, after.Round(time.Second))
}
