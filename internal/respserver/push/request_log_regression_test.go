package push

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPIHome/internal/respserver/dispatch"
)

// assertRequestLogsStayInLogDir fails when any file under root was created outside workDir/logs.
func assertRequestLogsStayInLogDir(t *testing.T, root string, workDir string) []string {
	t.Helper()
	logDir := filepath.Join(workDir, "logs")
	var inside []string
	errWalk := filepath.WalkDir(root, func(path string, entry fs.DirEntry, errEntry error) error {
		if errEntry != nil {
			return errEntry
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Dir(path) != logDir {
			t.Errorf("request log written outside log dir: %s", path)
			return nil
		}
		inside = append(inside, entry.Name())
		return nil
	})
	if errWalk != nil {
		t.Fatalf("walk %s: %v", root, errWalk)
	}
	return inside
}

func TestHandleRequestLogRejectsPathTraversalRequestID(t *testing.T) {
	for _, tt := range []struct {
		name    string
		payload string
	}{
		{name: "payload request_id", payload: `{"request_id":"/../../../escaped","request_log":"URL: /v1/responses\n"}`},
		{name: "x-request-id header", payload: `{"headers":{"x-request-id":["/../../../escaped"]},"request_log":"URL: /v1/responses\n"}`},
		{name: "x-cpa-request-id header", payload: `{"headers":{"x-cpa-request-id":"../../escaped"},"request_log":"URL: /v1/responses\n"}`},
		{name: "backslash request_id", payload: `{"request_id":"..\\..\\escaped","request_log":"URL: /v1/responses\n"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			workDir := filepath.Join(root, "a", "b")
			if errMkdir := os.MkdirAll(workDir, 0o755); errMkdir != nil {
				t.Fatalf("mkdir work dir: %v", errMkdir)
			}
			t.Chdir(workDir)

			reply := handleRequestLog(context.Background(), dispatch.Env{ClientIP: "10.0.0.5"}, []string{"RPUSH", "request-log", tt.payload})
			if reply.Kind != dispatch.ReplyKindInteger || reply.Integer != 1 {
				t.Fatalf("reply = %+v, want integer 1", reply)
			}

			inside := assertRequestLogsStayInLogDir(t, root, workDir)
			if len(inside) != 1 {
				t.Fatalf("request log files in log dir = %v, want 1", inside)
			}
			if strings.Contains(inside[0], "escaped") || strings.Contains(inside[0], "..") {
				t.Fatalf("filename %q kept the unsafe request id", inside[0])
			}
			if !regexp.MustCompile(`^10\.0\.0\.5-v1-responses-\d{4}-\d{2}-\d{2}T\d{6}-[0-9a-f]{8}\.log$`).MatchString(inside[0]) {
				t.Fatalf("filename %q does not use a generated request id", inside[0])
			}
		})
	}
}

func TestHandleRequestLogKeepsSafeRequestIDFilenames(t *testing.T) {
	for _, requestID := range []string{"0000002a", "018f3a5b-1234-7abc-def0-12345678abcd", "req_1.v2"} {
		t.Run(requestID, func(t *testing.T) {
			t.Chdir(t.TempDir())

			payload := `{"request_id":"` + requestID + `","request_log":"URL: /v1/chat/completions\n"}`
			reply := handleRequestLog(context.Background(), dispatch.Env{ClientIP: "10.0.0.5"}, []string{"RPUSH", "request-log", payload})
			if reply.Kind != dispatch.ReplyKindInteger || reply.Integer != 1 {
				t.Fatalf("reply = %+v, want integer 1", reply)
			}
			files, errGlob := filepath.Glob("logs/10.0.0.5-v1-chat-completions-*-" + requestID + ".log")
			if errGlob != nil {
				t.Fatalf("glob request log: %v", errGlob)
			}
			if len(files) != 1 {
				t.Fatalf("request log files for %q = %d, want 1", requestID, len(files))
			}
		})
	}
}

func TestWriteRequestLogFileRejectsEscapingFilename(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	if errMkdir := os.MkdirAll(workDir, 0o755); errMkdir != nil {
		t.Fatalf("mkdir work dir: %v", errMkdir)
	}
	t.Chdir(workDir)

	for _, filename := range []string{"../escaped.log", "x-/../../escaped.log", `..\escaped.log`} {
		if errWrite := writeRequestLogFile(filename, "content"); errWrite == nil {
			t.Fatalf("writeRequestLogFile(%q) error = nil, want error", filename)
		}
	}
	if inside := assertRequestLogsStayInLogDir(t, root, workDir); len(inside) != 0 {
		t.Fatalf("unexpected request log files: %v", inside)
	}
}
