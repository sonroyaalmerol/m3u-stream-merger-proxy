package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRootHandlerDispatch(t *testing.T) {
	handler := rootHandler(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user_info":{"auth":1}}`))
	})

	cases := []struct {
		target string
		code   int
		body   string
	}{
		{"/?username=u&password=p&action=get_live_streams", 200, `{"user_info":{"auth":1}}`},
		{"/?username=u&password=p", 200, `{"user_info":{"auth":1}}`},
		{"/", 404, "404 page not found\n"},
		{"/?action=", 404, "404 page not found\n"},
		{"/anything", 404, "404 page not found\n"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.target, nil)
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != c.code || rec.Body.String() != c.body {
			t.Errorf("%s: got %d %q, want %d %q", c.target, rec.Code, rec.Body.String(), c.code, c.body)
		}
	}
}
