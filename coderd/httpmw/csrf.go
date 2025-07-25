package httpmw

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/justinas/nosurf"
	"golang.org/x/xerrors"

	"github.com/coder/coder/v2/codersdk"
)

// CSRF is a middleware that verifies that a CSRF token is present in the request
// for non-GET requests.
// If enforce is false, then CSRF enforcement is disabled. We still want
// to include the CSRF middleware because it will set the CSRF cookie.
func CSRF(cookieCfg codersdk.HTTPCookieConfig) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		mw := nosurf.New(next)
		mw.SetBaseCookie(*cookieCfg.Apply(&http.Cookie{Path: "/", HttpOnly: true}))
		mw.SetFailureHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sessCookie, err := r.Cookie(codersdk.SessionTokenCookie)
			if err == nil &&
				r.Header.Get(codersdk.SessionTokenHeader) != "" &&
				r.Header.Get(codersdk.SessionTokenHeader) != sessCookie.Value {
				// If a user is using header authentication and cookie auth, but the values
				// do not match, the cookie value takes priority.
				// At the very least, return a more helpful error to the user.
				http.Error(w,
					fmt.Sprintf("CSRF error encountered. Authentication via %q cookie and %q header detected, but the values do not match. "+
						"To resolve this issue ensure the values used in both match, or only use one of the authentication methods. "+
						"You can also try clearing your cookies if this error persists.",
						codersdk.SessionTokenCookie, codersdk.SessionTokenHeader),
					http.StatusBadRequest)
				return
			}

			http.Error(w, "Something is wrong with your CSRF token. Please refresh the page. If this error persists, try clearing your cookies.", http.StatusBadRequest)
		}))

		mw.ExemptRegexp(regexp.MustCompile("/api/v2/users/first"))

		// Exempt all requests that do not require CSRF protection.
		// All GET requests are exempt by default.
		mw.ExemptPath("/api/v2/csp/reports")

		// This should not be required?
		mw.ExemptRegexp(regexp.MustCompile("/api/v2/users/first"))

		// Agent authenticated routes
		mw.ExemptRegexp(regexp.MustCompile("api/v2/workspaceagents/me/*"))
		mw.ExemptRegexp(regexp.MustCompile("api/v2/workspaceagents/*"))
		// Workspace Proxy routes
		mw.ExemptRegexp(regexp.MustCompile("api/v2/workspaceproxies/me/*"))
		// Derp routes
		mw.ExemptRegexp(regexp.MustCompile("derp/*"))
		// Scim
		mw.ExemptRegexp(regexp.MustCompile("api/v2/scim/*"))
		// Provisioner daemon routes
		mw.ExemptRegexp(regexp.MustCompile("/organizations/[^/]+/provisionerdaemons/*"))

		mw.ExemptFunc(func(r *http.Request) bool {
			// Only enforce CSRF on API routes.
			if !strings.HasPrefix(r.URL.Path, "/api") {
				return true
			}

			// CSRF only affects requests that automatically attach credentials via a cookie.
			// If no cookie is present, then there is no risk of CSRF.
			//nolint:govet
			sessCookie, err := r.Cookie(codersdk.SessionTokenCookie)
			if xerrors.Is(err, http.ErrNoCookie) {
				return true
			}

			if token := r.Header.Get(codersdk.SessionTokenHeader); token == sessCookie.Value {
				// If the cookie and header match, we can assume this is the same as just using the
				// custom header auth. Custom header auth can bypass CSRF, as CSRF attacks
				// cannot add custom headers.
				return true
			}

			if token := r.URL.Query().Get(codersdk.SessionTokenCookie); token == sessCookie.Value {
				// If the auth is set in a url param and matches the cookie, it
				// is the same as just using the url param.
				return true
			}

			if r.Header.Get(codersdk.ProvisionerDaemonPSK) != "" {
				// If present, the provisioner daemon also is providing an api key
				// that will make them exempt from CSRF. But this is still useful
				// for enumerating the external auths.
				return true
			}

			if r.Header.Get(codersdk.ProvisionerDaemonKey) != "" {
				// If present, the provisioner daemon also is providing an api key
				// that will make them exempt from CSRF. But this is still useful
				// for enumerating the external auths.
				return true
			}

			// RFC 6750 Bearer Token authentication is exempt from CSRF
			// as it uses custom headers that cannot be set by malicious sites
			if authHeader := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
				return true
			}

			// If the X-CSRF-TOKEN header is set, we can exempt the func if it's valid.
			// This is the CSRF check.
			sent := r.Header.Get("X-CSRF-TOKEN")
			if sent != "" {
				return nosurf.VerifyToken(nosurf.Token(r), sent)
			}
			return false
		})
		return mw
	}
}
