package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// check answers the full example's auth check by the token after "Bearer " in the authorization header, which
// auth.send passes on. A request with no token it knows, or none at all, is denied with 401, so a row that wants the
// check not to be asked (the example's auth.only_with_header) fails when the proxy asks it anyway.
func (b *backend) check(w http.ResponseWriter, r *http.Request) {
	switch strings.TrimPrefix(r.Header.Get("authorization"), "Bearer ") {
	case "allow":
		// x-user-check reports what the proxy told the check, so a scenario reads it back from the service's echo.
		// x-other is outside auth.accept, so the proxy must not pass it on to the service.
		w.Header().Set("x-user-id", "u-1")
		w.Header().Set("x-tier-level", "gold")
		w.Header().Set("x-other", "not-accepted")
		w.Header().Set("x-user-check", fmt.Sprintf("%s %s action=%s reports=%s country=%s",
			r.Header.Get("x-forwarded-method"), r.Header.Get("x-forwarded-uri"), r.Header.Get("x-gateway-action"),
			r.Header.Get("x-gateway-reports"), r.Header.Get("x-country")))
		w.WriteHeader(http.StatusOK)
	case "deny-401":
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "denied by the check")
	case "deny-403":
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, "denied by the check")
	case "status-503":
		w.WriteHeader(http.StatusServiceUnavailable)
	case "hang":
		time.Sleep(b.hang)
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "denied by the check")
	}
}
