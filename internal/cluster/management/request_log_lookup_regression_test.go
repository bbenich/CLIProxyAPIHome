package management

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

func writeRequestLogLookupFixture(t *testing.T, name string, content string, modified time.Time) {
	t.Helper()
	path := filepath.Join(homeLogDirectory, name)
	if errWrite := os.WriteFile(path, []byte(content), 0o644); errWrite != nil {
		t.Fatalf("write %s: %v", name, errWrite)
	}
	if errChtimes := os.Chtimes(path, modified, modified); errChtimes != nil {
		t.Fatalf("set %s modification time: %v", name, errChtimes)
	}
}

func TestDownloadRequestLogByIDMatchesOnlyExactRequestLogFiles(t *testing.T) {
	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()

	t.Chdir(t.TempDir())
	if errMkdir := os.Mkdir(homeLogDirectory, 0o755); errMkdir != nil {
		t.Fatalf("mkdir logs: %v", errMkdir)
	}
	base := time.Date(2026, 5, 29, 1, 2, 3, 0, time.UTC)
	// usage.log is the newest file, so a bare suffix match would prefer it.
	writeRequestLogLookupFixture(t, "usage.log", "usage secrets\n", base.Add(time.Hour))
	writeRequestLogLookupFixture(t, "10.0.0.5-v1-responses-2026-05-29T010203-0000abc2.log", "cpa request\n", base)
	writeRequestLogLookupFixture(t, "10.0.0.5-v1-responses-2026-05-29T010204-9f3e1a7b.log", "home generated request\n", base)

	handler := NewHandler(cluster.NewRepository(db), nil, "192.0.2.10", 0)
	engine := gin.New()
	engine.GET("/request-log-by-id/:id", handler.DownloadRequestLogByID)

	for _, tt := range []struct {
		name      string
		requestID string
		status    int
		body      string
	}{
		{name: "CPA 8-hex ID", requestID: "0000abc2", status: http.StatusOK, body: "cpa request\n"},
		{name: "Home generated ID", requestID: "9f3e1a7b", status: http.StatusOK, body: "home generated request\n"},
		{name: "usage log name", requestID: "usage", status: http.StatusNotFound},
		{name: "single character suffix of usage log", requestID: "e", status: http.StatusNotFound},
		{name: "single character suffix of another request", requestID: "2", status: http.StatusNotFound},
		{name: "partial suffix of another request", requestID: "abc2", status: http.StatusNotFound},
		{name: "dot dot", requestID: "a..b", status: http.StatusBadRequest},
		{name: "unsupported character", requestID: "abc%20def", status: http.StatusBadRequest},
		{name: "backslash", requestID: `x%5Cusage`, status: http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/request-log-by-id/"+tt.requestID+"?home_ip=192.0.2.10", nil)
			engine.ServeHTTP(resp, req)

			if resp.Code != tt.status {
				t.Fatalf("status = %d, want %d, body = %s", resp.Code, tt.status, resp.Body.String())
			}
			if tt.status == http.StatusOK && resp.Body.String() != tt.body {
				t.Fatalf("body = %q, want %q", resp.Body.String(), tt.body)
			}
		})
	}
}

func TestFindRequestLogFileRejectsLooseMatches(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 5, 29, 1, 2, 3, 0, time.UTC)
	for name, modified := range map[string]time.Time{
		"usage.log": base.Add(time.Hour),
		"10.0.0.5-v1-responses-2026-05-29T010203-0000abc2.log":                             base,
		"10.0.0.5-v1-responses-2026-05-29T010204-018f3a5b-1234-7abc-def0-12345678abcd.log": base,
	} {
		path := filepath.Join(dir, name)
		if errWrite := os.WriteFile(path, []byte("x\n"), 0o644); errWrite != nil {
			t.Fatalf("write %s: %v", name, errWrite)
		}
		if errChtimes := os.Chtimes(path, modified, modified); errChtimes != nil {
			t.Fatalf("set %s modification time: %v", name, errChtimes)
		}
	}

	for _, requestID := range []string{"usage", "e", "2", "abc2", ".log", "../usage", "a..b"} {
		if name, _, errFind := findRequestLogFile(dir, requestID); !errors.Is(errFind, os.ErrNotExist) {
			t.Fatalf("findRequestLogFile(%q) = %q, %v; want os.ErrNotExist", requestID, name, errFind)
		}
	}
	for requestID, want := range map[string]string{
		"0000abc2":                             "10.0.0.5-v1-responses-2026-05-29T010203-0000abc2.log",
		"018f3a5b-1234-7abc-def0-12345678abcd": "10.0.0.5-v1-responses-2026-05-29T010204-018f3a5b-1234-7abc-def0-12345678abcd.log",
		"5678abcd":                             "10.0.0.5-v1-responses-2026-05-29T010204-018f3a5b-1234-7abc-def0-12345678abcd.log",
	} {
		name, _, errFind := findRequestLogFile(dir, requestID)
		if errFind != nil || name != want {
			t.Fatalf("findRequestLogFile(%q) = %q, %v; want %q", requestID, name, errFind, want)
		}
	}
}
