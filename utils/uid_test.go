package utils

import (
	"testing"
)

func TestGetStreamUID(t *testing.T) {
	testCases := []struct {
		streamName string
		expected   string
	}{
		{"testStreamName", "dGVzdFN0cmVhbU5hbWU="},
		// Add more test cases as needed
	}

	for _, tc := range testCases {
		actual := GetStreamUID(tc.streamName)
		if actual != tc.expected {
			t.Errorf("GetStreamUID(%s) = %s; expected %s", tc.streamName, actual, tc.expected)
		}
	}
}

func TestGetStreamName(t *testing.T) {
	testCases := []struct {
		streamUID string
		expected  string
	}{
		{"dGVzdFN0cmVhbU5hbWU=", "testStreamName"},
		// Add more test cases as needed
	}

	for _, tc := range testCases {
		actual := GetStreamName(tc.streamUID)
		if actual != tc.expected {
			t.Errorf("GetStreamName(%s) = %s; expected %s", tc.streamUID, actual, tc.expected)
		}
	}
}

func TestGetStreamName_ErrorCase(t *testing.T) {
	// Test case for error handling when decoding fails
	invalidStreamUID := "invalidBase64"
	expected := ""

	actual := GetStreamName(invalidStreamUID)
	if actual != expected {
		t.Errorf("GetStreamName(%s) = %s; expected %s", invalidStreamUID, actual, expected)
	}
}
