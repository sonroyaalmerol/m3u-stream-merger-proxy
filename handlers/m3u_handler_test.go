package handlers

import (
	"m3u-stream-merger/logger"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func setupTest() (*M3UHandler, *httptest.ResponseRecorder, *http.Request) {
	handler := NewM3UHandler(&logger.DefaultLogger{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	return handler, recorder, request
}

func TestM3UHandler_NoAuth(t *testing.T) {
	// Setup
	os.Setenv("CREDENTIALS", "")
	handler, recorder, request := setupTest()

	// Test
	handler.ServeHTTP(recorder, request)

	// Assert
	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, recorder.Code)
	}
}

func TestM3UHandler_BasicAuth(t *testing.T) {
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
			handler, recorder, request := setupTest()

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

func TestM3UHandler_ExpirationDate(t *testing.T) {
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
			name:        "Invalid date format",
			credentials: "user1:pass1:invalid-date",
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
			handler, recorder, request := setupTest()

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

func TestM3UHandler_Headers(t *testing.T) {
	// Setup
	handler, recorder, request := setupTest()

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
