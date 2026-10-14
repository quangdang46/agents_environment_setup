package installer

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Verbose must actually say something: a --verbose that adds no output is a
// flag documenting nothing. The two lines that matter to a diagnosis are the
// URL and the checksum the download is going to be judged against.
func TestGithubReleaseVerboseLogsURL(t *testing.T) {
	t.Parallel()

	asset := tgz(t, map[string]string{"aes": "#!/bin/sh\necho aes\n"})
	fixture := newServer(t, asset)

	var log bytes.Buffer
	g := newInstaller(fixture.URL, t.TempDir())
	g.SetVerboseLog(true, &log)

	if err := g.Install(context.Background(), actionFor(fixture)); err != nil {
		t.Fatalf("Install: %v", err)
	}

	got := log.String()
	for _, want := range []string{"fetch " + fixture.URL, fixture.Digest} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Errorf("verbose log missing %q:\n%s", want, got)
		}
	}
}

// Without --verbose the same run logs nothing: the default stays quiet, and
// a run's log is readable rather than a transcript.
func TestGithubReleaseSilentByDefault(t *testing.T) {
	t.Parallel()

	asset := tgz(t, map[string]string{"aes": "#!/bin/sh\necho aes\n"})
	fixture := newServer(t, asset)

	var log bytes.Buffer
	g := newInstaller(fixture.URL, t.TempDir())
	// SetVerboseLog with verbose=false is what a non-verbose run does; the
	// writer is still attached, so this is the flag under test and not the
	// absence of a writer.
	g.SetVerboseLog(false, &log)

	if err := g.Install(context.Background(), actionFor(fixture)); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if log.Len() != 0 {
		t.Errorf("non-verbose run logged %q, want nothing", log.String())
	}
}

// The retry lines ride the same writer. A slow run explains itself without
// the success path becoming chatty: one failed attempt produces the verbose
// URL lines plus exactly one retry line, while a success produces no retry
// line at all.
func TestVerboseRetryLineRidesTheSameWriter(t *testing.T) {
	t.Parallel()

	fast := &retrier{
		delays: []time.Duration{0, 0},
		sleep:  func(_ context.Context, d time.Duration) error { return nil },
	}

	body := tgz(t, map[string]string{"aes": "#!/bin/sh\necho aes\n"})
	fixture := &serverFixture{URL: "", Digest: sumOf(body), Asset: body}

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	fixture.URL = srv.URL

	var log bytes.Buffer
	g := newInstaller(srv.URL, t.TempDir())
	g.retry = fast
	g.SetVerboseLog(true, &log)

	if err := g.Install(context.Background(), actionFor(fixture)); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if hits != 2 {
		t.Fatalf("hits = %d, want 2 (one failure, one retry)", hits)
	}
	if n := strings.Count(log.String(), "attempt 1/2"); n != 1 {
		t.Errorf("retry lines = %d, want exactly 1:\n%s", n, log.String())
	}
}
