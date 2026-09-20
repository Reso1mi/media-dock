package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerAuthProtectsHandlerAndLeavesHealthToCaller(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := BearerAuth(next, "secret-token")

	tests := []struct {
		name          string
		authorization string
		status        int
	}{
		{name: "missing", status: http.StatusUnauthorized},
		{name: "wrong token", authorization: "Bearer other", status: http.StatusUnauthorized},
		{name: "valid lowercase scheme", authorization: "bearer secret-token", status: http.StatusNoContent},
		{name: "valid mixed case scheme", authorization: "BeArEr secret-token", status: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
			if test.authorization != "" {
				request.Header.Set("Authorization", test.authorization)
			}
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d", recorder.Code, test.status)
			}
			if test.status == http.StatusUnauthorized && recorder.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("missing WWW-Authenticate header")
			}
		})
	}
}

func TestBearerAuthEmptyTokenLeavesExplicitDevelopmentHandlerUnchanged(t *testing.T) {
	called := false
	handler := BearerAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}), "")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil))
	if recorder.Code != http.StatusNoContent || !called {
		t.Fatalf("empty-token handler was not passed through: status=%d called=%v", recorder.Code, called)
	}
}
