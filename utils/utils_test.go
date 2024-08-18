package utils

import (
	"testing"
)

func TestGetStreamUrl(t *testing.T) {
	testCases := []struct {
		slug     string
		expected string
	}{
		{"test-stream-name", "dGVzdC1zdHJlYW0tbmFtZQ=="},
		// Add more test cases as needed
	}

	for _, tc := range testCases {
		actual := GetStreamUrl(tc.slug)
		if actual != tc.expected {
			t.Errorf("GetStreamUrl(%s) = %s; expected %s", tc.slug, actual, tc.expected)
		}
	}
}

func TestGetStreamSlugFromUrl(t *testing.T) {
	testCases := []struct {
		streamUID string
		expected  string
	}{
		{"dGVzdC1zdHJlYW0tbmFtZQ==", "test-stream-name"},
		// Add more test cases as needed
	}

	for _, tc := range testCases {
		actual := GetStreamSlugFromUrl(tc.streamUID)
		if actual != tc.expected {
			t.Errorf("GetStreamSlugFromUrl(%s) = %s; expected %s", tc.streamUID, actual, tc.expected)
		}
	}
}

func TestGetStreamSlugFromUrl_ErrorCase(t *testing.T) {
	// Test case for error handling when decoding fails
	invalidStreamUID := "invalidBase64"
	expected := ""

	actual := GetStreamSlugFromUrl(invalidStreamUID)
	if actual != expected {
		t.Errorf("GetStreamSlugFromUrl(%s) = %s; expected %s", invalidStreamUID, actual, expected)
	}
}
