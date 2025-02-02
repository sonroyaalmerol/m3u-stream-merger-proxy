package handlers

import (
	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setupTest(t *testing.T) (*M3UHTTPHandler, *httptest.ResponseRecorder, *http.Request) {
	handler := NewM3UHTTPHandler(&logger.DefaultLogger{})
	config.SetConfig(&config.Config{
		DataPath: "/",
	})
	tempDir, err := os.MkdirTemp("", "m3u-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer func() {
		t.Log("Cleaning up temporary directory:", tempDir)
		if err := os.RemoveAll(tempDir); err != nil {
			t.Errorf("Failed to cleanup temp directory: %v", err)
		}
	}()

	// Set up test environment
	testDataPath := filepath.Join(tempDir, "data")
	t.Log("Setting up test data path:", testDataPath)
	if err := os.MkdirAll(testDataPath, 0755); err != nil {
		t.Fatalf("Failed to create test data directory: %v", err)
	}

	tempPath := filepath.Join(testDataPath, "temp")
	t.Log("Creating temp directory:", tempPath)
	if err := os.MkdirAll(tempPath, 0755); err != nil {
		t.Fatalf("Failed to create streams directory: %v", err)
	}

	config.SetConfig(&config.Config{
		TempPath: tempPath,
		DataPath: testDataPath,
	})

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
	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, recorder.Code)
	}
}

func TestM3UHTTPHandler_BasicAuth(t *testing.T) {
	tests := []struct {
		name        string
		credentials string
		username    string
		password    string
		wantStatus  int
	}{
		{
			name:        "Valid credentials",
			credentials: "user1:pass1|user2:pass2",
			username:    "user1",
			password:    "pass1",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "Invalid password",
			credentials: "user1:pass1",
			username:    "user1",
			password:    "wrongpass",
			wantStatus:  http.StatusForbidden,
		},
		{
			name:        "Invalid username",
			credentials: "user1:pass1",
			username:    "wronguser",
			password:    "pass1",
			wantStatus:  http.StatusForbidden,
		},
		{
			name:        "Missing credentials",
			credentials: "user1:pass1",
			username:    "",
			password:    "",
			wantStatus:  http.StatusForbidden,
		},
		{
			name:        "Case insensitive username",
			credentials: "User1:pass1",
			username:    "user1",
			password:    "pass1",
			wantStatus:  http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			os.Setenv("CREDENTIALS", tt.credentials)
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
	tomorrow := time.Now().Add(24 * time.Hour).Format(time.DateOnly)
	yesterday := time.Now().Add(-24 * time.Hour).Format(time.DateOnly)

	tests := []struct {
		name        string
		credentials string
		username    string
		password    string
		wantStatus  int
	}{
		{
			name:        "Valid credentials with future expiration",
			credentials: "user1:pass1:" + tomorrow,
			username:    "user1",
			password:    "pass1",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "Expired credentials",
			credentials: "user1:pass1:" + yesterday,
			username:    "user1",
			password:    "pass1",
			wantStatus:  http.StatusForbidden,
		},
		{
			name:        "Multiple users with different expiration dates",
			credentials: "user1:pass1:" + yesterday + "|user2:pass2:" + tomorrow,
			username:    "user2",
			password:    "pass2",
			wantStatus:  http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			os.Setenv("CREDENTIALS", tt.credentials)
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
