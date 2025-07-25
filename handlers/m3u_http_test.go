package handlers

import (
	"context"
	"encoding/json"
	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/sourceproc"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setupTest(t *testing.T) (*M3UHTTPHandler, *httptest.ResponseRecorder, *http.Request) {
	tempDir, err := os.MkdirTemp("", "m3u-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(tempDir); err != nil {
			t.Errorf("Failed to cleanup temp directory: %v", err)
		}
	})

	// Set up test environment
	testDataPath := filepath.Join(tempDir, "data")
	if err := os.MkdirAll(testDataPath, 0755); err != nil {
		t.Fatalf("Failed to create test data directory: %v", err)
	}

	tempPath := filepath.Join(testDataPath, "temp")
	if err := os.MkdirAll(tempPath, 0755); err != nil {
		t.Fatalf("Failed to create streams directory: %v", err)
	}

	config.SetConfig(&config.Config{
		TempPath: tempPath,
		DataPath: testDataPath,
	})

	processor := sourceproc.NewProcessor()
	err = processor.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	handler := NewM3UHTTPHandler(&logger.DefaultLogger{}, processor.GetResultPath())

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	return handler, recorder, request
}

func TestM3UHTTPHandler_NoAuth(t *testing.T) {
	// Setup
	os.Setenv("CREDENTIALS", "")
	handler, recorder, request := setupTest(t)

	// Test
	handler.ServeHTTP(recorder, request)

	// Assert
	if recorder.Code != http.StatusNotFound {
		t.Errorf("Expected status code %d, got %d", http.StatusNotFound, recorder.Code)
	}
}

func TestM3UHTTPHandler_BasicAuth(t *testing.T) {
	creds := []Credential{
		{Username: "user1", Password: "pass1"},
		{Username: "user2", Password: "pass2"},
	}
	credsJSON, _ := json.Marshal(creds)

	tests := []struct {
		name       string
		username   string
		password   string
		wantStatus int
	}{
		{
			name:       "Valid credentials",
			username:   "user1",
			password:   "pass1",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "Invalid password",
			username:   "user1",
			password:   "wrongpass",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "Invalid username",
			username:   "wronguser",
			password:   "pass1",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "Missing credentials",
			username:   "",
			password:   "",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "Case insensitive username",
			username:   "User1",
			password:   "pass1",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			os.Setenv("CREDENTIALS", string(credsJSON))
			handler, recorder, request := setupTest(t)

			// Add auth parameters
			q := request.URL.Query()
			q.Add("username", tt.username)
			q.Add("password", tt.password)
			request.URL.RawQuery = q.Encode()

			// Test
			handler.ServeHTTP(recorder, request)

			// Assert
			if recorder.Code != tt.wantStatus {
				t.Errorf("Expected status code %d, got %d", tt.wantStatus, recorder.Code)
			}
		})
	}
}

func TestM3UHTTPHandler_ExpirationDate(t *testing.T) {
	creds := []Credential{
		{Username: "user1", Password: "pass1", Expiration: time.Now().Add(24 * time.Hour)},
		{Username: "user2", Password: "pass2", Expiration: time.Now().Add(-24 * time.Hour)},
	}
	credsJSON, _ := json.Marshal(creds)

	tests := []struct {
		name       string
		username   string
		password   string
		wantStatus int
	}{
		{
			name:       "Valid credentials with future expiration",
			username:   "user1",
			password:   "pass1",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "Expired credentials",
			username:   "user2",
			password:   "pass2",
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			os.Setenv("CREDENTIALS", string(credsJSON))
			handler, recorder, request := setupTest(t)

			// Add auth parameters
			q := request.URL.Query()
			q.Add("username", tt.username)
			q.Add("password", tt.password)
			request.URL.RawQuery = q.Encode()

			// Test
			handler.ServeHTTP(recorder, request)

			// Assert
			if recorder.Code != tt.wantStatus {
				t.Errorf("Expected status code %d, got %d", tt.wantStatus, recorder.Code)
			}
		})
	}
}

func TestM3UHTTPHandler_Headers(t *testing.T) {
	// Setup
	handler, recorder, request := setupTest(t)

	// Test
	handler.ServeHTTP(recorder, request)

	// Assert headers
	expectedHeaders := map[string]string{
		"Access-Control-Allow-Origin": "*",
		"Content-Type":                "text/plain; charset=utf-8",
	}

	for header, expectedValue := range expectedHeaders {
		if value := recorder.Header().Get(header); value != expectedValue {
			t.Errorf("Expected header %s to be %s, got %s", header, expectedValue, value)
		}
	}
}
