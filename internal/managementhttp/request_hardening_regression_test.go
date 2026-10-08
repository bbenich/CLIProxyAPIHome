package managementhttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	appconfig "github.com/router-for-me/CLIProxyAPIHome/internal/config"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
	"golang.org/x/crypto/bcrypt"
)

const regressionManagementKey = "regression-management-key"

// countingBody yields size bytes (a prefix followed by 'A' filler) and counts
// how many bytes the server actually consumed.
type countingBody struct {
	prefix string
	size   int64
	read   atomic.Int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	offset := b.read.Load()
	if offset >= b.size {
		return 0, io.EOF
	}
	if remaining := b.size - offset; int64(len(p)) > remaining {
		p = p[:remaining]
	}
	for i := range p {
		position := offset + int64(i)
		if position < int64(len(b.prefix)) {
			p[i] = b.prefix[position]
		} else {
			p[i] = 'A'
		}
	}
	b.read.Add(int64(len(p)))
	return len(p), nil
}

func (b *countingBody) Close() error { return nil }

// newOversizedRequest builds a request whose body is size bytes long. When
// declareLength is false the body is sent with unknown length (chunked).
func newOversizedRequest(method string, target string, contentType string, prefix string, size int64, declareLength bool) (*http.Request, *countingBody) {
	body := &countingBody{prefix: prefix, size: size}
	req := httptest.NewRequest(method, target, nil)
	req.Body = body
	req.ContentLength = -1
	if declareLength {
		req.ContentLength = size
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.RemoteAddr = "127.0.0.1:12345"
	return req, body
}

func bcryptManagementKey(t *testing.T, key string) string {
	t.Helper()
	hash, errHash := bcrypt.GenerateFromPassword([]byte(key), bcrypt.MinCost)
	if errHash != nil {
		t.Fatalf("bcrypt.GenerateFromPassword() error = %v", errHash)
	}
	return string(hash)
}

// newClusterManagementBuild builds the database-backed management engine.
func newClusterManagementBuild(t *testing.T) (*BuildResult, *home.Runtime) {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	dir := t.TempDir()
	db, errOpen := cluster.OpenSQLite(context.Background(), filepath.Join(dir, "home.db"))
	if errOpen != nil {
		t.Fatalf("OpenSQLite() error = %v", errOpen)
	}
	sqlDB, errDB := db.DB()
	if errDB != nil {
		t.Fatalf("db.DB() error = %v", errDB)
	}
	t.Cleanup(func() {
		if errClose := sqlDB.Close(); errClose != nil {
			t.Errorf("close sqlite db: %v", errClose)
		}
	})
	if errMigrate := cluster.AutoMigrate(db); errMigrate != nil {
		t.Fatalf("AutoMigrate() error = %v", errMigrate)
	}
	repo := cluster.NewRepository(db)
	configPath := filepath.Join(dir, "config.yaml")
	content := "auth-dir: " + filepath.ToSlash(filepath.Join(dir, "auths")) + "\nremote-management:\n  secret-key: '" + bcryptManagementKey(t, regressionManagementKey) + "'\n"
	if errWrite := os.WriteFile(configPath, []byte(content), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}
	runtimeConfig, errLoad := appconfig.LoadConfigOptional(configPath, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	rt, errRuntime := home.NewRuntime(runtimeConfig)
	if errRuntime != nil {
		t.Fatalf("home.NewRuntime() error = %v", errRuntime)
	}
	built, errBuild := Build(configPath, WithDatabaseManagement(DatabaseManagementOption{
		Enabled:    true,
		Repository: repo,
		Runtime:    rt,
	}))
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}
	return built, rt
}

// newFileManagementBuild builds the file-backed management engine.
func newFileManagementBuild(t *testing.T) *BuildResult {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "port: 8317\nauth-dir: " + filepath.ToSlash(dir) + "\nremote-management: {secret-key: '" + regressionManagementKey + "'}\n"
	if errWrite := os.WriteFile(path, []byte(content), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}
	built, errBuild := Build(path)
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}
	return built
}

func TestOAuthCallbackRejectsOversizedUnauthenticatedBody(t *testing.T) {
	const bodySize = 8 << 20
	builds := map[string]func(t *testing.T) *BuildResult{
		"database": func(t *testing.T) *BuildResult {
			built, _ := newClusterManagementBuild(t)
			return built
		},
		"file": newFileManagementBuild,
	}
	for name, newBuild := range builds {
		t.Run(name, func(t *testing.T) {
			built := newBuild(t)
			for _, declareLength := range []bool{true, false} {
				req, body := newOversizedRequest(http.MethodPost, "/v8/management/oauth/callback", "application/json", `{"state":"`, bodySize, declareLength)
				rec := httptest.NewRecorder()
				built.Engine.ServeHTTP(rec, req)
				if rec.Code != http.StatusRequestEntityTooLarge {
					t.Fatalf("declareLength=%v: status = %d, want %d: %s", declareLength, rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
				}
				if read := body.read.Load(); read > 2*oauthCallbackMaxRequestBodyBytes {
					t.Fatalf("declareLength=%v: server read %d bytes of an unauthenticated callback body", declareLength, read)
				}
			}
		})
	}
}

func TestManagementGroupsRejectOversizedBodies(t *testing.T) {
	built, _ := newClusterManagementBuild(t)
	const oversized = (64 << 20) + 1
	for _, target := range []string{"/v0/management/auth-files", "/v8/management/credentials"} {
		req, body := newOversizedRequest(http.MethodPost, target, "application/json", "", oversized, true)
		req.Header.Set("Authorization", "Bearer "+regressionManagementKey)
		rec := httptest.NewRecorder()
		built.Engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("%s: status = %d, want %d: %s", target, rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
		}
		if read := body.read.Load(); read != 0 {
			t.Fatalf("%s: server read %d bytes of a body that declared an oversized length", target, read)
		}
	}
}

func TestManagementGroupLimitKeepsPerRouteLimits(t *testing.T) {
	built, _ := newClusterManagementBuild(t)
	body := `{"node_name":"` + strings.Repeat("x", 8<<10) + `"}`
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/nodes/node-1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+regressionManagementKey)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	built.Engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
}

func getManagementDebug(engine http.Handler, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/v0/management/debug", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

// runConcurrentManagementRequests issues authenticated requests in parallel;
// under -race it reports any unsynchronized SDK handler config writes.
func runConcurrentManagementRequests(t *testing.T, engine http.Handler) {
	t.Helper()
	const workers = 4
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rec := getManagementDebug(engine, regressionManagementKey); rec.Code != http.StatusOK {
				errs <- fmt.Errorf("status = %d: %s", rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()
	close(errs)
	for errRequest := range errs {
		t.Fatal(errRequest)
	}
}

func TestConcurrentClusterManagementRequestsDoNotRewriteHandlerConfig(t *testing.T) {
	built, _ := newClusterManagementBuild(t)
	// Warm up so the first config application is not part of the race window.
	if rec := getManagementDebug(built.Engine, regressionManagementKey); rec.Code != http.StatusOK {
		t.Fatalf("warm-up status = %d: %s", rec.Code, rec.Body.String())
	}
	runConcurrentManagementRequests(t, built.Engine)
}

func TestConcurrentFileManagementRequestsDoNotRewriteHandlerConfig(t *testing.T) {
	built := newFileManagementBuild(t)
	if rec := getManagementDebug(built.Engine, regressionManagementKey); rec.Code != http.StatusOK {
		t.Fatalf("warm-up status = %d: %s", rec.Code, rec.Body.String())
	}
	runConcurrentManagementRequests(t, built.Engine)
}

func TestClusterManagementSecretRotationTakesEffectImmediately(t *testing.T) {
	built, rt := newClusterManagementBuild(t)
	if rec := getManagementDebug(built.Engine, regressionManagementKey); rec.Code != http.StatusOK {
		t.Fatalf("old key before rotation: status = %d: %s", rec.Code, rec.Body.String())
	}

	const rotatedKey = "rotated-management-key"
	next := *rt.Config()
	next.RemoteManagement.SecretKey = bcryptManagementKey(t, rotatedKey)
	if errApply := rt.ApplyConfigFromCluster(context.Background(), &next); errApply != nil {
		t.Fatalf("ApplyConfigFromCluster() error = %v", errApply)
	}

	if rec := getManagementDebug(built.Engine, regressionManagementKey); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old key after rotation: status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if rec := getManagementDebug(built.Engine, rotatedKey); rec.Code != http.StatusOK {
		t.Fatalf("new key after rotation: status = %d: %s", rec.Code, rec.Body.String())
	}
}
