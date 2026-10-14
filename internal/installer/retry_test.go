package installer

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// A retry schedule substituted for the production one, plus a recorder that
// waits instead of sleeping. Both are restored by the test that set them.
func stubRetrySchedule(t *testing.T) *[]time.Duration {
	t.Helper()
	origDelays, origSleep := retryDelays, sleepFn
	schedule := []time.Duration{0, 0, 0}
	var slept []time.Duration

	retryDelays = schedule
	sleepFn = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	t.Cleanup(func() {
		retryDelays, sleepFn = origDelays, origSleep
	})
	return &slept
}

// createTempFile opens an empty file at path for the download tests. The
// destination must exist before doDownloadWithRetry runs: it rewinds and
// rewrites rather than creating.
func createTempFile(t *testing.T, path string) (*os.File, error) {
	t.Helper()
	return os.Create(path)
}

func TestDownloadRetriesThenSucceeds(t *testing.T) {
	slept := stubRetrySchedule(t)

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("asset bytes"))
	}))
	defer srv.Close()

	dst := t.TempDir() + "/asset"
	f, err := createTempFile(t, dst)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()

	if err := doDownloadWithRetry(context.Background(), srv.Client(), srv.URL+"/asset", f, nil); err != nil {
		t.Fatalf("doDownloadWithRetry = %v, want success after retries", err)
	}
	if hits != 3 {
		t.Errorf("hits = %d, want 3 (two failures, then success)", hits)
	}
	if len(*slept) == 0 {
		t.Error("no backoff was waited for between attempts")
	}
}

// A 404 is the same 404 in fifteen seconds. Retrying it only delays the
// answer, and the delay is what makes a user think aes hung.
func TestDownload404DoesNotRetry(t *testing.T) {
	stubRetrySchedule(t)

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	f := t.TempDir() + "/asset"
	file, err := createTempFile(t, f)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer file.Close()

	err = doDownloadWithRetry(context.Background(), srv.Client(), srv.URL+"/missing", file, nil)
	if err == nil {
		t.Fatal("error = nil, want a failure")
	}
	if hits != 1 {
		t.Errorf("hits = %d, want exactly 1 for a 404", hits)
	}
	var se *statusError
	if !errors.As(err, &se) || se.code != http.StatusNotFound {
		t.Errorf("error = %v, want a 404 statusError", err)
	}
}

// Retry-After tells us when the upstream is ready. The schedule's own delay
// is a guess; the header is not.
func TestDownloadHonoursRetryAfter(t *testing.T) {
	slept := stubRetrySchedule(t)

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	file, err := createTempFile(t, t.TempDir()+"/asset")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer file.Close()

	if err := doDownloadWithRetry(context.Background(), srv.Client(), srv.URL, file, nil); err != nil {
		t.Fatalf("doDownloadWithRetry = %v", err)
	}
	if len(*slept) == 0 || (*slept)[0] != 7*time.Second {
		t.Errorf("slept = %v, want [7s] from Retry-After", *slept)
	}
}

// A checksum mismatch means the bytes are intact and the manifest is stale.
// Re-downloading fetches the same wrong bytes three times, which is why
// verifyChecksum sits outside the retry boundary — and this test is what
// keeps it there.
func TestChecksumMismatchIsFatal(t *testing.T) {
	stubRetrySchedule(t)

	if retryable, _ := classifyDownloadErr(ErrChecksum, 0); retryable {
		t.Error("classifyDownloadErr(ErrChecksum) retryable = true, want false")
	}

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte("not what the manifest says"))
	}))
	defer srv.Close()

	file, err := createTempFile(t, t.TempDir()+"/asset")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer file.Close()
	if err := doDownloadWithRetry(context.Background(), srv.Client(), srv.URL, file, nil); err != nil {
		t.Fatalf("doDownloadWithRetry = %v", err)
	}
	hitsBeforeVerify := hits

	// The caller's checksum step runs once, after the download, and is not
	// part of the retried unit.
	if err := verifyChecksum(file.Name(), "00"); err == nil {
		t.Error("verifyChecksum = nil, want ErrChecksum for the wrong bytes")
	}
	if hits != hitsBeforeVerify {
		t.Errorf("hits grew from %d to %d across verify, want verify outside retry", hitsBeforeVerify, hits)
	}
}

// Retrying must not become "wait on a prompt nobody can answer": a cancelled
// context returns promptly rather than running out the schedule.
func TestRetryAbortsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	origRetryDelays := retryDelays
	retryDelays = []time.Duration{0, 1 * time.Second}
	t.Cleanup(func() { retryDelays = origRetryDelays })

	err := doDownloadWithRetry(ctx, &http.Client{}, "http://127.0.0.1:1/asset", nil, nil)
	if err == nil {
		t.Fatal("error = nil, want a failure")
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{in: "5", want: 5 * time.Second},
		{in: "", want: 0},
		{in: "0", want: 0},
		{in: "-3", want: 0},
		{in: "garbage", want: 0},
		{in: "Wed, 21 Oct 2015 07:28:00 GMT", want: 0},
		{in: "99999", want: maxRetryAfter},
	}
	for _, tc := range cases {
		if got := parseRetryAfter(tc.in); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// Transport-level failures are retryable; that is the curl 6|7|28|35|52|56
// set in Go types. A verification failure is not: retrying is the wrong
// reflex when a certificate did not validate.
func TestClassifyTransportErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "dns", err: &net.DNSError{Err: "no such host"}, want: true},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, want: true},
		{name: "generic", err: errors.New("boom"), want: false},
		{name: "cancelled", err: context.Canceled, want: false},
		{name: "deadline", err: context.DeadlineExceeded, want: false},
		{name: "checksum", err: ErrChecksum, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := classifyDownloadErr(tc.err, 0)
			if got != tc.want {
				t.Errorf("classifyDownloadErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// 5xx and 429 say "retry"; 4xx say "the answer will not change".
func TestClassifyStatusCodes(t *testing.T) {
	retryable := []int{
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	}
	for _, code := range retryable {
		if got, _ := classifyDownloadErr(&statusError{code: code, status: http.StatusText(code)}, code); !got {
			t.Errorf("status %d classified fatal, want retryable", code)
		}
	}

	fatal := []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusGone,
	}
	for _, code := range fatal {
		if got, _ := classifyDownloadErr(&statusError{code: code, status: http.StatusText(code)}, code); got {
			t.Errorf("status %d classified retryable, want fatal", code)
		}
	}
}
