package apptest

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httputil"
	"net/url"
	"path"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/xerrors"

	"github.com/coder/coder/v2/coderd/coderdtest"
	"github.com/coder/coder/v2/coderd/jwtutils"
	"github.com/coder/coder/v2/coderd/rbac"
	"github.com/coder/coder/v2/coderd/workspaceapps"
	"github.com/coder/coder/v2/codersdk"
	"github.com/coder/coder/v2/codersdk/workspacesdk"
	"github.com/coder/coder/v2/testutil"
)

// Run runs the entire workspace app test suite against deployments minted
// by the provided factory.
//
// appHostIsPrimary is true if the app host is also the primary coder API
// server. This disables any tests that test API passthrough or rely on the
// app server not being the API server.
// nolint:revive
func Run(t *testing.T, appHostIsPrimary bool, factory DeploymentFactory) {
	setupProxyTest := func(t *testing.T, opts *DeploymentOptions) *Details {
		return setupProxyTestWithFactory(t, factory, opts)
	}

	t.Run("ReconnectingPTY", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" {
			// This might be our implementation, or ConPTY itself.  It's
			// difficult to find extensive tests for it, so it seems like it
			// could be either.
			t.Skip("ConPTY appears to be inconsistent on Windows.")
		}

		t.Run("OK", func(t *testing.T) {
			t.Parallel()
			appDetails := setupProxyTest(t, nil)

			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			// Run the test against the path app hostname since that's where the
			// reconnecting-pty proxy server we want to test is mounted.
			client := appDetails.AppClient(t)
			testReconnectingPTY(ctx, t, client, appDetails.Agent.ID, "")
			assertWorkspaceLastUsedAtUpdated(t, appDetails)
		})

		t.Run("SignedTokenQueryParameter", func(t *testing.T) {
			t.Parallel()

			if appHostIsPrimary {
				t.Skip("Tickets are not used for terminal requests on the primary.")
			}

			appDetails := setupProxyTest(t, nil)

			u := *appDetails.PathAppBaseURL
			if u.Scheme == "http" {
				u.Scheme = "ws"
			} else {
				u.Scheme = "wss"
			}
			u.Path = fmt.Sprintf("/api/v2/workspaceagents/%s/pty", appDetails.Agent.ID.String())

			ctx := testutil.Context(t, testutil.WaitLong)
			issueRes, err := appDetails.SDKClient.IssueReconnectingPTYSignedToken(ctx, codersdk.IssueReconnectingPTYSignedTokenRequest{
				URL:     u.String(),
				AgentID: appDetails.Agent.ID,
			})
			require.NoError(t, err)

			// Make an unauthenticated client.
			unauthedAppClient := codersdk.New(appDetails.AppClient(t).URL)
			testReconnectingPTY(ctx, t, unauthedAppClient, appDetails.Agent.ID, issueRes.SignedToken)
			assertWorkspaceLastUsedAtUpdated(t, appDetails)
		})
	})

	t.Run("WorkspaceAppsProxyPath", func(t *testing.T) {
		t.Parallel()

		t.Run("Disabled", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, &DeploymentOptions{
				DisablePathApps: true,
			})

			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, appDetails.PathAppURL(appDetails.Apps.Owner).String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusForbidden, resp.StatusCode)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Contains(t, string(body), "Path-based applications are disabled")
			// Even though path-based apps are disabled, the request should indicate
			// that the workspace was used.
			assertWorkspaceLastUsedAtNotUpdated(t, appDetails)
		})

		t.Run("LoginWithoutAuthOnPrimary", func(t *testing.T) {
			t.Parallel()

			if !appHostIsPrimary {
				t.Skip("This test only applies when testing apps on the primary.")
			}

			appDetails := setupProxyTest(t, nil)
			unauthedClient := appDetails.AppClient(t)
			unauthedClient.SetSessionToken("")

			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			u := appDetails.PathAppURL(appDetails.Apps.Owner).String()
			resp, err := requestWithRetries(ctx, t, unauthedClient, http.MethodGet, u, nil)
			require.NoError(t, err)
			defer resp.Body.Close()

			require.Equal(t, http.StatusSeeOther, resp.StatusCode)
			loc, err := resp.Location()
			require.NoError(t, err)
			require.True(t, loc.Query().Has("message"))
			require.True(t, loc.Query().Has("redirect"))
			assertWorkspaceLastUsedAtNotUpdated(t, appDetails)
		})

		t.Run("LoginWithoutAuthOnProxy", func(t *testing.T) {
			t.Parallel()

			if appHostIsPrimary {
				t.Skip("This test only applies when testing apps on workspace proxies.")
			}

			appDetails := setupProxyTest(t, nil)
			unauthedClient := appDetails.AppClient(t)
			unauthedClient.SetSessionToken("")

			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			u := appDetails.PathAppURL(appDetails.Apps.Owner)
			resp, err := requestWithRetries(ctx, t, unauthedClient, http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()

			require.Equal(t, http.StatusSeeOther, resp.StatusCode)
			loc, err := resp.Location()
			require.NoError(t, err)
			require.Equal(t, appDetails.SDKClient.URL.Host, loc.Host)
			require.Equal(t, "/api/v2/applications/auth-redirect", loc.Path)

			redirectURIStr := loc.Query().Get("redirect_uri")
			require.NotEmpty(t, redirectURIStr)
			redirectURI, err := url.Parse(redirectURIStr)
			require.NoError(t, err)

			require.Equal(t, u.Scheme, redirectURI.Scheme)
			require.Equal(t, u.Host, redirectURI.Host)
			// TODO(@dean): I have no idea how but the trailing slash on this
			// request is getting stripped.
			require.Equal(t, u.Path, redirectURI.Path+"/")
			require.Equal(t, u.RawQuery, redirectURI.RawQuery)
			assertWorkspaceLastUsedAtNotUpdated(t, appDetails)
		})

		t.Run("NoAccessShould404", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, nil)
			userClient, _ := coderdtest.CreateAnotherUser(t, appDetails.SDKClient, appDetails.FirstUser.OrganizationID, rbac.RoleMember())
			userAppClient := appDetails.AppClient(t)
			userAppClient.SetSessionToken(userClient.SessionToken())

			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			resp, err := requestWithRetries(ctx, t, userAppClient, http.MethodGet, appDetails.PathAppURL(appDetails.Apps.Owner).String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusNotFound, resp.StatusCode)
			// TODO(cian): A blocked request should not count as workspace usage.
			// assertWorkspaceLastUsedAtNotUpdated(t, appDetails.AppClient(t), appDetails)
		})

		t.Run("RedirectsWithSlash", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, nil)
			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			u := appDetails.PathAppURL(appDetails.Apps.Owner)
			u.Path = strings.TrimSuffix(u.Path, "/")
			resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode)
			// TODO(cian): The initial redirect should not count as workspace usage.
			// assertWorkspaceLastUsedAtNotUpdated(t, appDetails.AppClient(t), appDetails)
		})

		t.Run("RedirectsWithQuery", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, nil)
			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			u := appDetails.PathAppURL(appDetails.Apps.Owner)
			u.RawQuery = ""
			resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode)
			loc, err := resp.Location()
			require.NoError(t, err)
			require.Equal(t, proxyTestAppQuery, loc.RawQuery)
			// TODO(cian): The initial redirect should not count as workspace usage.
			// assertWorkspaceLastUsedAtNotUpdated(t, appDetails.AppClient(t), appDetails)
		})

		t.Run("Proxies", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, nil)
			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			u := appDetails.PathAppURL(appDetails.Apps.Owner)
			resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, proxyTestAppBody, string(body))
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var appTokenCookie *http.Cookie
			for _, c := range resp.Cookies() {
				if c.Name == codersdk.SignedAppTokenCookie {
					appTokenCookie = c
					break
				}
			}
			require.NotNil(t, appTokenCookie, "no signed app token cookie in response")
			require.Equal(t, appTokenCookie.Path, u.Path, "incorrect path on app token cookie")

			// Ensure the signed app token cookie is valid.
			appTokenClient := appDetails.AppClient(t)
			appTokenClient.SetSessionToken("")
			appTokenClient.HTTPClient.Jar, err = cookiejar.New(nil)
			require.NoError(t, err)
			appTokenClient.HTTPClient.Jar.SetCookies(u, []*http.Cookie{appTokenCookie})

			resp, err = requestWithRetries(ctx, t, appTokenClient, http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err = io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, proxyTestAppBody, string(body))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			assertWorkspaceLastUsedAtUpdated(t, appDetails)
		})

		t.Run("ProxiesHTTPS", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, &DeploymentOptions{
				ServeHTTPS: true,
			})

			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			u := appDetails.PathAppURL(appDetails.Apps.Owner)
			resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, proxyTestAppBody, string(body))
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var appTokenCookie *http.Cookie
			for _, c := range resp.Cookies() {
				if c.Name == codersdk.SignedAppTokenCookie {
					appTokenCookie = c
					break
				}
			}
			require.NotNil(t, appTokenCookie, "no signed app token cookie in response")
			require.Equal(t, appTokenCookie.Path, u.Path, "incorrect path on app token cookie")

			// Ensure the signed app token cookie is valid.
			appTokenClient := appDetails.AppClient(t)
			appTokenClient.SetSessionToken("")
			appTokenClient.HTTPClient.Jar, err = cookiejar.New(nil)
			require.NoError(t, err)
			appTokenClient.HTTPClient.Jar.SetCookies(u, []*http.Cookie{appTokenCookie})

			resp, err = requestWithRetries(ctx, t, appTokenClient, http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err = io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, proxyTestAppBody, string(body))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			assertWorkspaceLastUsedAtUpdated(t, appDetails)
		})

		t.Run("BlocksMe", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, nil)
			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			app := appDetails.Apps.Owner
			app.Username = codersdk.Me

			resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, appDetails.PathAppURL(app).String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusNotFound, resp.StatusCode)

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Contains(t, string(body), "must be accessed with the full username, not @me")
			assertWorkspaceLastUsedAtNotUpdated(t, appDetails)
		})

		t.Run("ForwardsIP", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, nil)
			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, appDetails.PathAppURL(appDetails.Apps.Owner).String(), nil, func(r *http.Request) {
				r.Header.Set("Cf-Connecting-IP", "1.1.1.1")
			})
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, proxyTestAppBody, string(body))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, "1.1.1.1,127.0.0.1", resp.Header.Get("X-Forwarded-For"))
			assertWorkspaceLastUsedAtUpdated(t, appDetails)
		})

		t.Run("ProxyError", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, nil)
			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			resp, err := appDetails.AppClient(t).Request(ctx, http.MethodGet, appDetails.PathAppURL(appDetails.Apps.Fake).String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusBadGateway, resp.StatusCode)
			// An valid authenticated attempt to access a workspace app
			// should count as usage regardless of success.
			assertWorkspaceLastUsedAtUpdated(t, appDetails)
		})

		t.Run("NoProxyPort", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, nil)
			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			resp, err := appDetails.AppClient(t).Request(ctx, http.MethodGet, appDetails.PathAppURL(appDetails.Apps.Port).String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			// TODO(@deansheather): This should be 400. There's a todo in the
			// resolve request code to fix this.
			require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
			assertWorkspaceLastUsedAtNotUpdated(t, appDetails)
		})

		t.Run("BadJWT", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, nil)
			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			u := appDetails.PathAppURL(appDetails.Apps.Owner)
			resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, proxyTestAppBody, string(body))
			require.Equal(t, http.StatusOK, resp.StatusCode)

			appTokenCookie := findCookie(resp.Cookies(), codersdk.SignedAppTokenCookie)
			require.NotNil(t, appTokenCookie, "no signed app token cookie in response")
			require.Equal(t, appTokenCookie.Path, u.Path, "incorrect path on app token cookie")

			object, err := jose.ParseSigned(appTokenCookie.Value, []jose.SignatureAlgorithm{jwtutils.SigningAlgo})
			require.NoError(t, err)
			require.Len(t, object.Signatures, 1)

			// Parse the payload.
			var tok workspaceapps.SignedToken
			//nolint:gosec
			err = json.Unmarshal(object.UnsafePayloadWithoutVerification(), &tok)
			require.NoError(t, err)

			appTokenClient := appDetails.AppClient(t)
			apiKey := appTokenClient.SessionToken()
			appTokenClient.SetSessionToken("")
			appTokenClient.HTTPClient.Jar, err = cookiejar.New(nil)
			require.NoError(t, err)
			// Sign the token with an old-style key.
			appTokenCookie.Value = generateBadJWT(t, tok)
			appTokenClient.HTTPClient.Jar.SetCookies(u,
				[]*http.Cookie{
					appTokenCookie,
					{
						Name:  codersdk.PathAppSessionTokenCookie,
						Value: apiKey,
					},
				},
			)

			resp, err = requestWithRetries(ctx, t, appTokenClient, http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err = io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, proxyTestAppBody, string(body))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			assertWorkspaceLastUsedAtUpdated(t, appDetails)

			// Since the old token is invalid, the signed app token cookie should have a new value.
			newTokenCookie := findCookie(resp.Cookies(), codersdk.SignedAppTokenCookie)
			require.NotEqual(t, appTokenCookie.Value, newTokenCookie.Value)
		})
	})

	t.Run("WorkspaceApplicationCORS", func(t *testing.T) {
		t.Parallel()

		const external = "https://example.com"

		unauthenticatedClient := func(t *testing.T, appDetails *Details) *codersdk.Client {
			c := appDetails.AppClient(t)
			c.SetSessionToken("")
			return c
		}

		authenticatedClient := func(t *testing.T, appDetails *Details) *codersdk.Client {
			uc, _ := coderdtest.CreateAnotherUser(t, appDetails.SDKClient, appDetails.FirstUser.OrganizationID, rbac.RoleMember())
			c := appDetails.AppClient(t)
			c.SetSessionToken(uc.SessionToken())
			return c
		}

		ownSubdomain := func(details *Details, app App) string {
			url := details.SubdomainAppURL(app)
			return url.Scheme + "://" + url.Host
		}

		externalOrigin := func(*Details, App) string {
			return external
		}

		tests := []struct {
			name                 string
			app                  func(details *Details) App
			client               func(t *testing.T, appDetails *Details) *codersdk.Client
			behavior             codersdk.CORSBehavior
			httpMethod           string
			origin               func(details *Details, app App) string
			expectedStatusCode   int
			checkRequestHeaders  func(t *testing.T, origin string, req http.Header)
			checkResponseHeaders func(t *testing.T, origin string, resp http.Header)
		}{
			// Public
			{ // fails
				// The default behavior is to accept preflight requests from the request origin if it matches the app's own subdomain.
				name:               "Default/Public/Preflight/Subdomain",
				app:                func(details *Details) App { return details.Apps.PublicCORSDefault },
				behavior:           codersdk.CORSBehaviorSimple,
				client:             unauthenticatedClient,
				httpMethod:         http.MethodOptions,
				origin:             ownSubdomain,
				expectedStatusCode: http.StatusOK,
				checkResponseHeaders: func(t *testing.T, origin string, resp http.Header) {
					assert.Equal(t, origin, resp.Get("Access-Control-Allow-Origin"))
					assert.Contains(t, resp.Get("Access-Control-Allow-Methods"), http.MethodGet)
					assert.Equal(t, "true", resp.Get("Access-Control-Allow-Credentials"))
				},
			},
			{ // passes
				// The default behavior is to reject preflight requests from origins other than the app's own subdomain.
				name:               "Default/Public/Preflight/External",
				app:                func(details *Details) App { return details.Apps.PublicCORSDefault },
				behavior:           codersdk.CORSBehaviorSimple,
				client:             unauthenticatedClient,
				httpMethod:         http.MethodOptions,
				origin:             externalOrigin,
				expectedStatusCode: http.StatusOK,
				checkResponseHeaders: func(t *testing.T, origin string, resp http.Header) {
					// We don't add a valid Allow-Origin header for requests we won't proxy.
					assert.Empty(t, resp.Get("Access-Control-Allow-Origin"))
				},
			},
			{ // fails
				// A request without an Origin header would be rejected by an actual browser since it lacks CORS headers.
				name:               "Default/Public/GET/NoOrigin",
				app:                func(details *Details) App { return details.Apps.PublicCORSDefault },
				behavior:           codersdk.CORSBehaviorSimple,
				client:             unauthenticatedClient,
				origin:             func(*Details, App) string { return "" },
				httpMethod:         http.MethodGet,
				expectedStatusCode: http.StatusOK,
				checkResponseHeaders: func(t *testing.T, origin string, resp http.Header) {
					assert.Empty(t, resp.Get("Access-Control-Allow-Origin"))
					assert.Empty(t, resp.Get("Access-Control-Allow-Headers"))
					assert.Empty(t, resp.Get("Access-Control-Allow-Credentials"))
					// Added by the app handler.
					assert.Equal(t, "simple", resp.Get("X-CORS-Handler"))
				},
			},
			{ // fails
				// The passthru behavior will pass through the request headers to the upstream app.
				name:               "Passthru/Public/Preflight/Subdomain",
				app:                func(details *Details) App { return details.Apps.PublicCORSPassthru },
				behavior:           codersdk.CORSBehaviorPassthru,
				client:             unauthenticatedClient,
				origin:             ownSubdomain,
				httpMethod:         http.MethodOptions,
				expectedStatusCode: http.StatusOK,
				checkRequestHeaders: func(t *testing.T, origin string, req http.Header) {
					assert.Equal(t, origin, req.Get("Origin"))
					assert.Equal(t, "GET", req.Get("Access-Control-Request-Method"))
				},
				checkResponseHeaders: func(t *testing.T, origin string, resp http.Header) {
					assert.Equal(t, origin, resp.Get("Access-Control-Allow-Origin"))
					assert.Equal(t, http.MethodGet, resp.Get("Access-Control-Allow-Methods"))
					// Added by the app handler.
					assert.Equal(t, "passthru", resp.Get("X-CORS-Handler"))
				},
			},
			{ // fails
				// Identical to the previous test, but the origin is different.
				name:               "Passthru/Public/PreflightOther",
				app:                func(details *Details) App { return details.Apps.PublicCORSPassthru },
				behavior:           codersdk.CORSBehaviorPassthru,
				client:             unauthenticatedClient,
				origin:             externalOrigin,
				httpMethod:         http.MethodOptions,
				expectedStatusCode: http.StatusOK,
				checkRequestHeaders: func(t *testing.T, origin string, req http.Header) {
					assert.Equal(t, origin, req.Get("Origin"))
					assert.Equal(t, "GET", req.Get("Access-Control-Request-Method"))
					assert.Equal(t, "X-Got-Host", req.Get("Access-Control-Request-Headers"))
				},
				checkResponseHeaders: func(t *testing.T, origin string, resp http.Header) {
					assert.Equal(t, origin, resp.Get("Access-Control-Allow-Origin"))
					assert.Equal(t, http.MethodGet, resp.Get("Access-Control-Allow-Methods"))
					// Added by the app handler.
					assert.Equal(t, "passthru", resp.Get("X-CORS-Handler"))
				},
			},
			{
				// A request without an Origin header would be rejected by an actual browser since it lacks CORS headers.
				name:               "Passthru/Public/GET/NoOrigin",
				app:                func(details *Details) App { return details.Apps.PublicCORSPassthru },
				behavior:           codersdk.CORSBehaviorPassthru,
				client:             unauthenticatedClient,
				origin:             func(*Details, App) string { return "" },
				httpMethod:         http.MethodGet,
				expectedStatusCode: http.StatusOK,
				checkResponseHeaders: func(t *testing.T, origin string, resp http.Header) {
					assert.Empty(t, resp.Get("Access-Control-Allow-Origin"))
					assert.Empty(t, resp.Get("Access-Control-Allow-Headers"))
					assert.Empty(t, resp.Get("Access-Control-Allow-Credentials"))
					// Added by the app handler.
					assert.Equal(t, "passthru", resp.Get("X-CORS-Handler"))
				},
			},
			// Authenticated
			{
				// Same behavior as Default/Public/Preflight/Subdomain.
				name:               "Default/Authenticated/Preflight/Subdomain",
				app:                func(details *Details) App { return details.Apps.AuthenticatedCORSDefault },
				behavior:           codersdk.CORSBehaviorSimple,
				client:             authenticatedClient,
				origin:             ownSubdomain,
				httpMethod:         http.MethodOptions,
				expectedStatusCode: http.StatusOK,
				checkResponseHeaders: func(t *testing.T, origin string, resp http.Header) {
					assert.Equal(t, origin, resp.Get("Access-Control-Allow-Origin"))
					assert.Contains(t, resp.Get("Access-Control-Allow-Methods"), http.MethodGet)
					assert.Equal(t, "true", resp.Get("Access-Control-Allow-Credentials"))
					assert.Equal(t, "X-Got-Host", resp.Get("Access-Control-Allow-Headers"))
				},
			},
			{
				// Same behavior as Default/Public/Preflight/External.
				name:               "Default/Authenticated/Preflight/External",
				app:                func(details *Details) App { return details.Apps.AuthenticatedCORSDefault },
				behavior:           codersdk.CORSBehaviorSimple,
				client:             authenticatedClient,
				origin:             externalOrigin,
				httpMethod:         http.MethodOptions,
				expectedStatusCode: http.StatusOK,
				checkResponseHeaders: func(t *testing.T, origin string, resp http.Header) {
					assert.Empty(t, resp.Get("Access-Control-Allow-Origin"))
				},
			},
			{
				// An authenticated request to the app is allowed from its own subdomain.
				name:               "Default/Authenticated/GET/Subdomain",
				app:                func(details *Details) App { return details.Apps.AuthenticatedCORSDefault },
				behavior:           codersdk.CORSBehaviorSimple,
				client:             authenticatedClient,
				origin:             ownSubdomain,
				httpMethod:         http.MethodGet,
				expectedStatusCode: http.StatusOK,
				checkResponseHeaders: func(t *testing.T, origin string, resp http.Header) {
					assert.Equal(t, origin, resp.Get("Access-Control-Allow-Origin"))
					assert.Equal(t, "true", resp.Get("Access-Control-Allow-Credentials"))
					// Added by the app handler.
					assert.Equal(t, "simple", resp.Get("X-CORS-Handler"))
				},
			},
			{
				// An authenticated request to the app is allowed from an external origin.
				// The origin doesn't match the app's own subdomain, so the CORS headers are not added.
				name:               "Default/Authenticated/GET/External",
				app:                func(details *Details) App { return details.Apps.AuthenticatedCORSDefault },
				behavior:           codersdk.CORSBehaviorSimple,
				client:             authenticatedClient,
				origin:             externalOrigin,
				httpMethod:         http.MethodGet,
				expectedStatusCode: http.StatusOK,
				checkResponseHeaders: func(t *testing.T, origin string, resp http.Header) {
					assert.Empty(t, resp.Get("Access-Control-Allow-Origin"))
					assert.Empty(t, resp.Get("Access-Control-Allow-Headers"))
					assert.Empty(t, resp.Get("Access-Control-Allow-Credentials"))
					// Added by the app handler.
					assert.Equal(t, "simple", resp.Get("X-CORS-Handler"))
				},
			},
			{
				// The request is rejected because the client is unauthenticated.
				name:               "Passthru/Unauthenticated/Preflight/Subdomain",
				app:                func(details *Details) App { return details.Apps.AuthenticatedCORSPassthru },
				behavior:           codersdk.CORSBehaviorPassthru,
				client:             unauthenticatedClient,
				origin:             ownSubdomain,
				httpMethod:         http.MethodOptions,
				expectedStatusCode: http.StatusSeeOther,
				checkResponseHeaders: func(t *testing.T, origin string, resp http.Header) {
					assert.NotEmpty(t, resp.Get("Location"))
				},
			},
			{
				// Same behavior as the above test, but the origin is different.
				name:               "Passthru/Unauthenticated/Preflight/External",
				app:                func(details *Details) App { return details.Apps.AuthenticatedCORSPassthru },
				behavior:           codersdk.CORSBehaviorPassthru,
				client:             unauthenticatedClient,
				origin:             externalOrigin,
				httpMethod:         http.MethodOptions,
				expectedStatusCode: http.StatusSeeOther,
				checkResponseHeaders: func(t *testing.T, origin string, resp http.Header) {
					assert.NotEmpty(t, resp.Get("Location"))
				},
			},
			{
				// The request is rejected because the client is unauthenticated.
				name:               "Passthru/Unauthenticated/GET/Subdomain",
				app:                func(details *Details) App { return details.Apps.AuthenticatedCORSPassthru },
				behavior:           codersdk.CORSBehaviorPassthru,
				client:             unauthenticatedClient,
				origin:             ownSubdomain,
				httpMethod:         http.MethodGet,
				expectedStatusCode: http.StatusSeeOther,
				checkResponseHeaders: func(t *testing.T, origin string, resp http.Header) {
					assert.NotEmpty(t, resp.Get("Location"))
				},
			},
			{
				// Same behavior as the above test, but the origin is different.
				name:               "Passthru/Unauthenticated/GET/External",
				app:                func(details *Details) App { return details.Apps.AuthenticatedCORSPassthru },
				behavior:           codersdk.CORSBehaviorPassthru,
				client:             unauthenticatedClient,
				origin:             externalOrigin,
				httpMethod:         http.MethodGet,
				expectedStatusCode: http.StatusSeeOther,
				checkResponseHeaders: func(t *testing.T, origin string, resp http.Header) {
					assert.NotEmpty(t, resp.Get("Location"))
				},
			},
			{
				// The request is allowed because the client is authenticated.
				name:               "Passthru/Authenticated/Preflight/Subdomain",
				app:                func(details *Details) App { return details.Apps.AuthenticatedCORSPassthru },
				behavior:           codersdk.CORSBehaviorPassthru,
				client:             authenticatedClient,
				origin:             ownSubdomain,
				httpMethod:         http.MethodOptions,
				expectedStatusCode: http.StatusOK,
				checkResponseHeaders: func(t *testing.T, origin string, resp http.Header) {
					assert.Equal(t, origin, resp.Get("Access-Control-Allow-Origin"))
					assert.Equal(t, http.MethodGet, resp.Get("Access-Control-Allow-Methods"))
					// Added by the app handler.
					assert.Equal(t, "passthru", resp.Get("X-CORS-Handler"))
				},
			},
			{
				// Same behavior as the above test, but the origin is different.
				name:               "Passthru/Authenticated/Preflight/External",
				app:                func(details *Details) App { return details.Apps.AuthenticatedCORSPassthru },
				behavior:           codersdk.CORSBehaviorPassthru,
				client:             authenticatedClient,
				origin:             externalOrigin,
				httpMethod:         http.MethodOptions,
				expectedStatusCode: http.StatusOK,
				checkResponseHeaders: func(t *testing.T, origin string, resp http.Header) {
					assert.Equal(t, origin, resp.Get("Access-Control-Allow-Origin"))
					assert.Equal(t, http.MethodGet, resp.Get("Access-Control-Allow-Methods"))
					// Added by the app handler.
					assert.Equal(t, "passthru", resp.Get("X-CORS-Handler"))
				},
			},
			{
				// The request is allowed because the client is authenticated.
				name:               "Passthru/Authenticated/GET/Subdomain",
				app:                func(details *Details) App { return details.Apps.AuthenticatedCORSPassthru },
				behavior:           codersdk.CORSBehaviorPassthru,
				client:             authenticatedClient,
				origin:             ownSubdomain,
				httpMethod:         http.MethodGet,
				expectedStatusCode: http.StatusOK,
				checkResponseHeaders: func(t *testing.T, origin string, resp http.Header) {
					assert.Equal(t, origin, resp.Get("Access-Control-Allow-Origin"))
					assert.Equal(t, http.MethodGet, resp.Get("Access-Control-Allow-Methods"))
					// Added by the app handler.
					assert.Equal(t, "passthru", resp.Get("X-CORS-Handler"))
				},
			},
			{
				// Same behavior as the above test, but the origin is different.
				name:               "Passthru/Authenticated/GET/External",
				app:                func(details *Details) App { return details.Apps.AuthenticatedCORSPassthru },
				behavior:           codersdk.CORSBehaviorPassthru,
				client:             authenticatedClient,
				origin:             externalOrigin,
				httpMethod:         http.MethodGet,
				expectedStatusCode: http.StatusOK,
				checkResponseHeaders: func(t *testing.T, origin string, resp http.Header) {
					assert.Equal(t, origin, resp.Get("Access-Control-Allow-Origin"))
					assert.Equal(t, http.MethodGet, resp.Get("Access-Control-Allow-Methods"))
					// Added by the app handler.
					assert.Equal(t, "passthru", resp.Get("X-CORS-Handler"))
				},
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				ctx := testutil.Context(t, testutil.WaitLong)

				var reqHeaders http.Header
				// Setup an HTTP handler which is the "app"; this handler conditionally responds
				// to requests based on the CORS behavior
				appDetails := setupProxyTest(t, &DeploymentOptions{
					handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						_, err := r.Cookie(codersdk.SessionTokenCookie)
						assert.ErrorIs(t, err, http.ErrNoCookie)

						// Store the request headers for later assertions
						reqHeaders = r.Header

						switch tc.behavior {
						case codersdk.CORSBehaviorPassthru:
							w.Header().Set("X-CORS-Handler", "passthru")

							// Only allow GET and OPTIONS requests
							if r.Method != http.MethodGet && r.Method != http.MethodOptions {
								w.WriteHeader(http.StatusMethodNotAllowed)
								return
							}

							// If the Origin header is present, add the CORS headers.
							if origin := r.Header.Get("Origin"); origin != "" {
								w.Header().Set("Access-Control-Allow-Credentials", "true")
								w.Header().Set("Access-Control-Allow-Origin", origin)
								w.Header().Set("Access-Control-Allow-Methods", http.MethodGet)
							}

							w.WriteHeader(http.StatusOK)
						case codersdk.CORSBehaviorSimple:
							w.Header().Set("X-CORS-Handler", "simple")
						}
					}),
				})

				// Update the template CORS behavior.
				b := tc.behavior
				template, err := appDetails.SDKClient.UpdateTemplateMeta(ctx, appDetails.Workspace.TemplateID, codersdk.UpdateTemplateMeta{
					CORSBehavior: &b,
				})
				require.NoError(t, err)
				require.Equal(t, tc.behavior, template.CORSBehavior)

				// Given: a client and a workspace app
				client := tc.client(t, appDetails)
				path := appDetails.SubdomainAppURL(tc.app(appDetails)).String()
				origin := tc.origin(appDetails, tc.app(appDetails))

				fmt.Println("method: ", tc.httpMethod)
				// When: a preflight request is made to an app with a specified CORS behavior
				resp, err := requestWithRetries(ctx, t, client, tc.httpMethod, path, nil, func(r *http.Request) {
					// Mimic non-browser clients that don't send the Origin header.
					if origin != "" {
						r.Header.Set("Origin", origin)
					}
					r.Header.Set("Access-Control-Request-Method", "GET")
					r.Header.Set("Access-Control-Request-Headers", "X-Got-Host")
				})
				require.NoError(t, err)
				defer resp.Body.Close()

				// Then: the request & response must match expectations
				assert.Equal(t, tc.expectedStatusCode, resp.StatusCode)
				assert.NoError(t, err)
				if tc.checkRequestHeaders != nil {
					tc.checkRequestHeaders(t, origin, reqHeaders)
				}
				tc.checkResponseHeaders(t, origin, resp.Header)
			})
		}
	})

	t.Run("WorkspaceApplicationAuth", func(t *testing.T) {
		t.Parallel()

		// The OK test checks the entire end-to-end flow of authentication.
		t.Run("End-to-End", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, nil)

			cases := []struct {
				name                   string
				appURL                 *url.URL
				sessionTokenCookieName string
			}{
				{
					name:                   "Subdomain",
					appURL:                 appDetails.SubdomainAppURL(appDetails.Apps.Owner),
					sessionTokenCookieName: codersdk.SubdomainAppSessionTokenCookie,
				},
				{
					name:                   "Path",
					appURL:                 appDetails.PathAppURL(appDetails.Apps.Owner),
					sessionTokenCookieName: codersdk.PathAppSessionTokenCookie,
				},
			}

			for _, c := range cases {
				if c.name == "Path" && appHostIsPrimary {
					// Workspace application auth does not apply to path apps
					// served from the primary access URL as no smuggling needs
					// to take place (they're already logged in with a session
					// token).
					continue
				}

				t.Run(c.name, func(t *testing.T) {
					t.Parallel()

					ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
					defer cancel()

					// Get the current user and API key.
					user, err := appDetails.SDKClient.User(ctx, codersdk.Me)
					require.NoError(t, err)
					currentAPIKey, err := appDetails.SDKClient.APIKeyByID(ctx, appDetails.FirstUser.UserID.String(), strings.Split(appDetails.SDKClient.SessionToken(), "-")[0])
					require.NoError(t, err)

					appClient := appDetails.AppClient(t)
					appClient.SetSessionToken("")

					// Try to load the application without authentication.
					u := *c.appURL
					u.Path = path.Join(u.Path, "/test")
					req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
					require.NoError(t, err)

					var resp *http.Response
					resp, err = doWithRetries(t, appClient, req)
					require.NoError(t, err)

					if !assert.Equal(t, http.StatusSeeOther, resp.StatusCode) {
						dump, err := httputil.DumpResponse(resp, true)
						require.NoError(t, err)
						t.Log(string(dump))
					}
					resp.Body.Close()

					// Check that the Location is correct.
					gotLocation, err := resp.Location()
					require.NoError(t, err)
					// This should always redirect to the primary access URL.
					require.Equal(t, appDetails.SDKClient.URL.Host, gotLocation.Host)
					require.Equal(t, "/api/v2/applications/auth-redirect", gotLocation.Path)
					require.Equal(t, u.String(), gotLocation.Query().Get("redirect_uri"))

					// Load the application auth-redirect endpoint.
					resp, err = requestWithRetries(ctx, t, appDetails.SDKClient, http.MethodGet, "/api/v2/applications/auth-redirect", nil, codersdk.WithQueryParam(
						"redirect_uri", u.String(),
					))
					require.NoError(t, err)
					defer resp.Body.Close()

					require.Equal(t, http.StatusSeeOther, resp.StatusCode)
					gotLocation, err = resp.Location()
					require.NoError(t, err)

					// Copy the query parameters and then check equality.
					u.RawQuery = gotLocation.RawQuery
					require.Equal(t, u, *gotLocation)

					// Verify the API key is set.
					encryptedAPIKey := gotLocation.Query().Get(workspaceapps.SubdomainProxyAPIKeyParam)
					require.NotEmpty(t, encryptedAPIKey, "no API key was set in the query parameters")

					// Decrypt the API key by following the request.
					t.Log("navigating to: ", gotLocation.String())
					req, err = http.NewRequestWithContext(ctx, "GET", gotLocation.String(), nil)
					require.NoError(t, err)
					resp, err = doWithRetries(t, appClient, req)
					require.NoError(t, err)
					resp.Body.Close()
					require.Equal(t, http.StatusSeeOther, resp.StatusCode)

					cookies := resp.Cookies()
					var cookie *http.Cookie
					for _, co := range cookies {
						if co.Name == c.sessionTokenCookieName {
							cookie = co
							break
						}
					}
					require.NotNil(t, cookie, "no app session token cookie was set")
					apiKey := cookie.Value

					// Fetch the API key from the API.
					apiKeyInfo, err := appDetails.SDKClient.APIKeyByID(ctx, appDetails.FirstUser.UserID.String(), strings.Split(apiKey, "-")[0])
					require.NoError(t, err)
					require.Equal(t, user.ID, apiKeyInfo.UserID)
					require.Equal(t, codersdk.LoginTypePassword, apiKeyInfo.LoginType)
					require.WithinDuration(t, currentAPIKey.ExpiresAt, apiKeyInfo.ExpiresAt, 5*time.Second)
					require.EqualValues(t, currentAPIKey.LifetimeSeconds, apiKeyInfo.LifetimeSeconds)

					// Verify the API key permissions
					appTokenAPIClient := codersdk.New(appDetails.SDKClient.URL)
					appTokenAPIClient.SetSessionToken(apiKey)
					appTokenAPIClient.HTTPClient.CheckRedirect = appDetails.SDKClient.HTTPClient.CheckRedirect
					appTokenAPIClient.HTTPClient.Transport = appDetails.SDKClient.HTTPClient.Transport

					var (
						canApplicationConnect = "can-create-application_connect"
						canReadUserMe         = "can-read-user-me"
					)
					authRes, err := appTokenAPIClient.AuthCheck(ctx, codersdk.AuthorizationRequest{
						Checks: map[string]codersdk.AuthorizationCheck{
							canApplicationConnect: {
								Object: codersdk.AuthorizationObject{
									ResourceType:   "workspace",
									OwnerID:        appDetails.FirstUser.UserID.String(),
									OrganizationID: appDetails.FirstUser.OrganizationID.String(),
								},
								Action: codersdk.ActionApplicationConnect,
							},
							canReadUserMe: {
								Object: codersdk.AuthorizationObject{
									ResourceType: "user",
									ResourceID:   appDetails.FirstUser.UserID.String(),
								},
								Action: codersdk.ActionRead,
							},
						},
					})
					require.NoError(t, err)

					require.True(t, authRes[canApplicationConnect])
					require.False(t, authRes[canReadUserMe])

					// Load the application page with the API key set.
					gotLocation, err = resp.Location()
					require.NoError(t, err)
					t.Log("navigating to: ", gotLocation.String())
					req, err = http.NewRequestWithContext(ctx, "GET", gotLocation.String(), nil)
					require.NoError(t, err)
					req.Header.Set(codersdk.SessionTokenHeader, apiKey)
					resp, err = doWithRetries(t, appClient, req)
					require.NoError(t, err)
					resp.Body.Close()
					require.Equal(t, http.StatusOK, resp.StatusCode)
				})

				t.Run("BadJWE", func(t *testing.T) {
					t.Parallel()

					ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
					defer cancel()

					currentKeyStr := appDetails.SDKClient.SessionToken()
					appClient := appDetails.AppClient(t)
					appClient.SetSessionToken("")
					u := *c.appURL
					u.Path = path.Join(u.Path, "/test")
					badToken := generateBadJWE(t, workspaceapps.EncryptedAPIKeyPayload{
						APIKey: currentKeyStr,
					})

					u.RawQuery = (url.Values{
						workspaceapps.SubdomainProxyAPIKeyParam: {badToken},
					}).Encode()

					req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
					require.NoError(t, err)

					var resp *http.Response
					resp, err = doWithRetries(t, appClient, req)
					require.NoError(t, err)
					defer resp.Body.Close()
					require.Equal(t, http.StatusBadRequest, resp.StatusCode)
					body, err := io.ReadAll(resp.Body)
					require.NoError(t, err)
					require.Contains(t, string(body), "Could not decrypt API key. Please remove the query parameter and try again.")
				})
			}
		})
	})

	t.Run("WorkspaceAppsProxySubdomainHostnamePrefix/OK", func(t *testing.T) {
		t.Parallel()

		appDetails := setupProxyTest(t, nil)

		// Try to load the owner app with a prefix.
		ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
		defer cancel()

		prefixedOwnerApp := appDetails.Apps.Owner
		prefixedOwnerApp.Prefix = "some---prefix---"

		u := appDetails.SubdomainAppURL(prefixedOwnerApp)
		require.Contains(t, u.Host, prefixedOwnerApp.Prefix)

		resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, u.String(), nil)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, resp.Header.Get("X-Got-Host"), u.Host)

		// Parse the returned signed token to verify that it contains the
		// prefix.
		var appTokenCookie *http.Cookie
		for _, c := range resp.Cookies() {
			if c.Name == codersdk.SignedAppTokenCookie {
				appTokenCookie = c
				break
			}
		}
		require.NotNil(t, appTokenCookie, "no signed app token cookie in response")

		// Parse the JWT without verifying it (since we can't access the key
		// from this test).
		object, err := jose.ParseSigned(appTokenCookie.Value, []jose.SignatureAlgorithm{jwtutils.SigningAlgo})
		require.NoError(t, err)
		require.Len(t, object.Signatures, 1)

		// Parse the payload.
		var tok workspaceapps.SignedToken
		//nolint:gosec
		err = json.Unmarshal(object.UnsafePayloadWithoutVerification(), &tok)
		require.NoError(t, err)

		// Verify the prefix is in the token.
		require.Equal(t, prefixedOwnerApp.Prefix, tok.Request.Prefix)

		// Ensure the signed app token cookie is valid by making a request with
		// it with no session token.
		appTokenClient := appDetails.AppClient(t)
		appTokenClient.SetSessionToken("")
		appTokenClient.HTTPClient.Jar, err = cookiejar.New(nil)
		require.NoError(t, err)
		appTokenClient.HTTPClient.Jar.SetCookies(u, []*http.Cookie{appTokenCookie})

		resp, err = requestWithRetries(ctx, t, appTokenClient, http.MethodGet, u.String(), nil)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, resp.Header.Get("X-Got-Host"), u.Host)
		assertWorkspaceLastUsedAtUpdated(t, appDetails)
	})

	t.Run("WorkspaceAppsProxySubdomainHostnamePrefix/Different", func(t *testing.T) {
		t.Parallel()

		appDetails := setupProxyTest(t, nil)

		// Try to load the owner app with a prefix.
		ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
		defer cancel()

		prefixedOwnerApp := appDetails.Apps.Owner
		t.Log(appDetails.SubdomainAppURL(prefixedOwnerApp))
		prefixedOwnerApp.Prefix = "some---prefix---"
		t.Log(appDetails.SubdomainAppURL(prefixedOwnerApp))

		u := appDetails.SubdomainAppURL(prefixedOwnerApp)
		require.Contains(t, u.Host, prefixedOwnerApp.Prefix)

		resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, u.String(), nil)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// Find the cookie.
		var appTokenCookie *http.Cookie
		for _, c := range resp.Cookies() {
			if c.Name == codersdk.SignedAppTokenCookie {
				appTokenCookie = c
				break
			}
		}
		require.NotNil(t, appTokenCookie, "no signed app token cookie in response")

		// Ensure the signed app token cookie is valid only for the given prefix
		// by making a request with it with no session token.
		appTokenClient := appDetails.AppClient(t)
		appTokenClient.SetSessionToken("")
		appTokenClient.HTTPClient.Jar, err = cookiejar.New(nil)
		require.NoError(t, err)
		appTokenClient.HTTPClient.Jar.SetCookies(u, []*http.Cookie{appTokenCookie})

		prefixedOwnerApp.Prefix = "different---"
		u = appDetails.SubdomainAppURL(prefixedOwnerApp)
		require.Contains(t, u.Host, prefixedOwnerApp.Prefix)

		resp, err = requestWithRetries(ctx, t, appTokenClient, http.MethodGet, u.String(), nil)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.NotEqual(t, http.StatusOK, resp.StatusCode)
		assertWorkspaceLastUsedAtUpdated(t, appDetails)
	})

	// This test ensures that the subdomain handler does nothing if
	// --app-hostname is not set by the admin.
	t.Run("WorkspaceAppsProxySubdomainPassthrough", func(t *testing.T) {
		t.Parallel()
		if !appHostIsPrimary {
			t.Skip("app hostname does not serve API")
		}
		// No Hostname set.
		appDetails := setupProxyTest(t, &DeploymentOptions{
			AppHost:              "",
			DisableSubdomainApps: true,
			noWorkspace:          true,
		})

		ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
		defer cancel()

		u := *appDetails.SDKClient.URL
		u.Host = "app--agent--workspace--username.test.coder.com"
		u.Path = "/api/v2/users/me"
		resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, u.String(), nil)
		require.NoError(t, err)
		defer resp.Body.Close()

		// Should look like a codersdk.User response.
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var user codersdk.User
		err = json.NewDecoder(resp.Body).Decode(&user)
		require.NoError(t, err)
		require.Equal(t, appDetails.FirstUser.UserID, user.ID)
	})

	// This test ensures that the subdomain handler blocks the request if it
	// looks like a workspace app request but the configured app hostname
	// differs from the request, or the request is not a valid app subdomain but
	// the hostname matches.
	t.Run("WorkspaceAppsProxySubdomainBlocked", func(t *testing.T) {
		t.Parallel()

		appDetails := setupProxyTest(t, &DeploymentOptions{
			noWorkspace: true,
		})

		t.Run("InvalidSubdomain", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			host := strings.Replace(appDetails.Options.AppHost, "*", "not-an-app-subdomain", 1)
			uri := fmt.Sprintf("http://%s/api/v2/users/me", host)
			resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, uri, nil)
			require.NoError(t, err)
			defer resp.Body.Close()

			// Should have a HTML error response.
			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Contains(t, string(body), "Could not parse subdomain application URL")
		})
	})

	t.Run("WorkspaceAppsProxySubdomain", func(t *testing.T) {
		t.Parallel()

		t.Run("NoAccessShould401", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, nil)
			userClient, _ := coderdtest.CreateAnotherUser(t, appDetails.SDKClient, appDetails.FirstUser.OrganizationID, rbac.RoleMember())
			userAppClient := appDetails.AppClient(t)
			userAppClient.SetSessionToken(userClient.SessionToken())

			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			resp, err := requestWithRetries(ctx, t, userAppClient, http.MethodGet, appDetails.SubdomainAppURL(appDetails.Apps.Owner).String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusNotFound, resp.StatusCode)
			assertWorkspaceLastUsedAtNotUpdated(t, appDetails)
		})

		t.Run("RedirectsWithSlash", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, nil)
			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			u := appDetails.SubdomainAppURL(appDetails.Apps.Owner)
			u.Path = ""
			u.RawQuery = ""
			resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode)

			loc, err := resp.Location()
			require.NoError(t, err)
			require.Equal(t, appDetails.SubdomainAppURL(appDetails.Apps.Owner).Path, loc.Path)
			assertWorkspaceLastUsedAtNotUpdated(t, appDetails)
		})

		t.Run("RedirectsWithQuery", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, nil)
			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			u := appDetails.SubdomainAppURL(appDetails.Apps.Owner)
			u.RawQuery = ""
			resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode)

			loc, err := resp.Location()
			require.NoError(t, err)
			require.Equal(t, appDetails.SubdomainAppURL(appDetails.Apps.Owner).RawQuery, loc.RawQuery)
			assertWorkspaceLastUsedAtNotUpdated(t, appDetails)
		})

		t.Run("Proxies", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, nil)
			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			u := appDetails.SubdomainAppURL(appDetails.Apps.Owner)
			resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, proxyTestAppBody, string(body))
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var appTokenCookie *http.Cookie
			for _, c := range resp.Cookies() {
				if c.Name == codersdk.SignedAppTokenCookie {
					appTokenCookie = c
					break
				}
			}
			require.NotNil(t, appTokenCookie, "no signed token cookie in response")
			require.Equal(t, appTokenCookie.Path, "/", "incorrect path on signed token cookie")

			// Ensure the signed app token cookie is valid.
			appTokenClient := appDetails.AppClient(t)
			appTokenClient.SetSessionToken("")
			appTokenClient.HTTPClient.Jar, err = cookiejar.New(nil)
			require.NoError(t, err)
			appTokenClient.HTTPClient.Jar.SetCookies(u, []*http.Cookie{appTokenCookie})

			resp, err = requestWithRetries(ctx, t, appTokenClient, http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err = io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, proxyTestAppBody, string(body))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			assertWorkspaceLastUsedAtUpdated(t, appDetails)
		})

		t.Run("ProxiesHTTPS", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, &DeploymentOptions{
				ServeHTTPS: true,
			})
			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			u := appDetails.SubdomainAppURL(appDetails.Apps.Owner)
			resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, proxyTestAppBody, string(body))
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var appTokenCookie *http.Cookie
			for _, c := range resp.Cookies() {
				if c.Name == codersdk.SignedAppTokenCookie {
					appTokenCookie = c
					break
				}
			}
			require.NotNil(t, appTokenCookie, "no signed token cookie in response")
			require.Equal(t, appTokenCookie.Path, "/", "incorrect path on signed token cookie")

			// Ensure the signed app token cookie is valid.
			appTokenClient := appDetails.AppClient(t)
			appTokenClient.SetSessionToken("")
			appTokenClient.HTTPClient.Jar, err = cookiejar.New(nil)
			require.NoError(t, err)
			appTokenClient.HTTPClient.Jar.SetCookies(u, []*http.Cookie{appTokenCookie})

			resp, err = requestWithRetries(ctx, t, appTokenClient, http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err = io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, proxyTestAppBody, string(body))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			assertWorkspaceLastUsedAtUpdated(t, appDetails)
		})

		t.Run("ProxiesPort", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, nil)
			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, appDetails.SubdomainAppURL(appDetails.Apps.Port).String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, proxyTestAppBody, string(body))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			assertWorkspaceLastUsedAtUpdated(t, appDetails)
		})

		t.Run("ProxyError", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, nil)
			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			resp, err := appDetails.AppClient(t).Request(ctx, http.MethodGet, appDetails.SubdomainAppURL(appDetails.Apps.Fake).String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusBadGateway, resp.StatusCode)
			assertWorkspaceLastUsedAtUpdated(t, appDetails)
		})

		t.Run("ProxyPortMinimumError", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, nil)
			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			app := appDetails.Apps.Port
			app.AppSlugOrPort = strconv.Itoa(workspacesdk.AgentMinimumListeningPort - 1)
			resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, appDetails.SubdomainAppURL(app).String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()

			// Should have an error response.
			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			var resBody codersdk.Response
			err = json.NewDecoder(resp.Body).Decode(&resBody)
			require.NoError(t, err)
			require.Contains(t, resBody.Message, "Coder reserves ports less than")
			assertWorkspaceLastUsedAtNotUpdated(t, appDetails)
		})

		t.Run("SuffixWildcardOK", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, &DeploymentOptions{
				AppHost: "*-suffix.test.coder.com",
			})

			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			u := appDetails.SubdomainAppURL(appDetails.Apps.Owner)
			t.Logf("url: %s", u)

			resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, proxyTestAppBody, string(body))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			assertWorkspaceLastUsedAtUpdated(t, appDetails)
		})

		t.Run("WildcardPortOK", func(t *testing.T) {
			t.Parallel()

			// Manually specifying a port should override the access url port on
			// the app host.
			appDetails := setupProxyTest(t, &DeploymentOptions{
				// Just throw both the wsproxy and primary to same url.
				AppHost:        "*.test.coder.com:4444",
				PrimaryAppHost: "*.test.coder.com:4444",
			})

			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			u := appDetails.SubdomainAppURL(appDetails.Apps.Owner)
			t.Logf("url: %s", u)
			require.Equal(t, "4444", u.Port(), "port should be 4444")

			// Assert the api response the UI uses has the port.
			apphost, err := appDetails.SDKClient.AppHost(ctx)
			require.NoError(t, err)
			require.Equal(t, "*.test.coder.com:4444", apphost.Host, "apphost has port")

			resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, proxyTestAppBody, string(body))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			assertWorkspaceLastUsedAtUpdated(t, appDetails)
		})

		t.Run("SuffixWildcardNotMatch", func(t *testing.T) {
			t.Parallel()

			t.Run("NoSuffix", func(t *testing.T) {
				t.Parallel()

				appDetails := setupProxyTest(t, &DeploymentOptions{
					AppHost: "*-suffix.test.coder.com",
				})

				ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
				defer cancel()

				u := appDetails.SubdomainAppURL(appDetails.Apps.Owner)
				// Replace the -suffix with nothing.
				u.Host = strings.Replace(u.Host, "-suffix", "", 1)
				t.Logf("url: %s", u)

				resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, u.String(), nil)
				require.NoError(t, err)
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)

				// It's probably rendering the dashboard or a 404 page, so only
				// ensure that the body doesn't match.
				require.NotContains(t, string(body), proxyTestAppBody)
				assertWorkspaceLastUsedAtNotUpdated(t, appDetails)
			})

			t.Run("DifferentSuffix", func(t *testing.T) {
				t.Parallel()

				appDetails := setupProxyTest(t, &DeploymentOptions{
					AppHost: "*-suffix.test.coder.com",
				})

				ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
				defer cancel()

				u := appDetails.SubdomainAppURL(appDetails.Apps.Owner)
				// Replace the -suffix with something else.
				u.Host = strings.Replace(u.Host, "-suffix", "-not-suffix", 1)
				t.Logf("url: %s", u)

				resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, u.String(), nil)
				require.NoError(t, err)
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)

				// It's probably rendering the dashboard, so only ensure that the body
				// doesn't match.
				require.NotContains(t, string(body), proxyTestAppBody)
				assertWorkspaceLastUsedAtNotUpdated(t, appDetails)
			})
		})

		t.Run("BadJWT", func(t *testing.T) {
			t.Parallel()

			appDetails := setupProxyTest(t, nil)
			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			u := appDetails.SubdomainAppURL(appDetails.Apps.Owner)
			resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, proxyTestAppBody, string(body))
			require.Equal(t, http.StatusOK, resp.StatusCode)

			appTokenCookie := findCookie(resp.Cookies(), codersdk.SignedAppTokenCookie)
			require.NotNil(t, appTokenCookie, "no signed token cookie in response")
			require.Equal(t, appTokenCookie.Path, "/", "incorrect path on signed token cookie")

			object, err := jose.ParseSigned(appTokenCookie.Value, []jose.SignatureAlgorithm{jwtutils.SigningAlgo})
			require.NoError(t, err)
			require.Len(t, object.Signatures, 1)

			// Parse the payload.
			var tok workspaceapps.SignedToken
			//nolint:gosec
			err = json.Unmarshal(object.UnsafePayloadWithoutVerification(), &tok)
			require.NoError(t, err)

			appTokenClient := appDetails.AppClient(t)
			apiKey := appTokenClient.SessionToken()
			appTokenClient.SetSessionToken("")
			appTokenClient.HTTPClient.Jar, err = cookiejar.New(nil)
			require.NoError(t, err)
			// Sign the token with an old-style key.
			appTokenCookie.Value = generateBadJWT(t, tok)
			appTokenClient.HTTPClient.Jar.SetCookies(u,
				[]*http.Cookie{
					appTokenCookie,
					{
						Name:  codersdk.SubdomainAppSessionTokenCookie,
						Value: apiKey,
					},
				},
			)

			// We should still be able to successfully proxy.
			resp, err = requestWithRetries(ctx, t, appTokenClient, http.MethodGet, u.String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err = io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, proxyTestAppBody, string(body))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			assertWorkspaceLastUsedAtUpdated(t, appDetails)

			// Since the old token is invalid, the signed app token cookie should have a new value.
			newTokenCookie := findCookie(resp.Cookies(), codersdk.SignedAppTokenCookie)
			require.NotEqual(t, appTokenCookie.Value, newTokenCookie.Value)
		})
	})

	t.Run("PortSharing", func(t *testing.T) {
		t.Run("NoShare", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			appDetails := setupProxyTest(t, nil)
			userClient, _ := coderdtest.CreateAnotherUser(t, appDetails.SDKClient, appDetails.FirstUser.OrganizationID, rbac.RoleMember())
			userAppClient := appDetails.AppClient(t)
			userAppClient.SetSessionToken(userClient.SessionToken())

			resp, err := requestWithRetries(ctx, t, userAppClient, http.MethodGet, appDetails.SubdomainAppURL(appDetails.Apps.Port).String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusNotFound, resp.StatusCode)
			assertWorkspaceLastUsedAtNotUpdated(t, appDetails)
		})

		t.Run("AuthenticatedOK", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			appDetails := setupProxyTest(t, nil)
			port, err := strconv.ParseInt(appDetails.Apps.Port.AppSlugOrPort, 10, 32)
			require.NoError(t, err)
			// set the port we have to be shared with authenticated users
			_, err = appDetails.SDKClient.UpsertWorkspaceAgentPortShare(ctx, appDetails.Workspace.ID, codersdk.UpsertWorkspaceAgentPortShareRequest{
				AgentName:  proxyTestAgentName,
				Port:       int32(port),
				ShareLevel: codersdk.WorkspaceAgentPortShareLevelAuthenticated,
				Protocol:   codersdk.WorkspaceAgentPortShareProtocolHTTP,
			})
			require.NoError(t, err)

			userClient, _ := coderdtest.CreateAnotherUser(t, appDetails.SDKClient, appDetails.FirstUser.OrganizationID, rbac.RoleMember())
			userAppClient := appDetails.AppClient(t)
			userAppClient.SetSessionToken(userClient.SessionToken())

			resp, err := requestWithRetries(ctx, t, userAppClient, http.MethodGet, appDetails.SubdomainAppURL(appDetails.Apps.Port).String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			assertWorkspaceLastUsedAtUpdated(t, appDetails)
		})

		t.Run("PublicOK", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			appDetails := setupProxyTest(t, nil)
			port, err := strconv.ParseInt(appDetails.Apps.Port.AppSlugOrPort, 10, 32)
			require.NoError(t, err)
			// set the port we have to be shared with public
			_, err = appDetails.SDKClient.UpsertWorkspaceAgentPortShare(ctx, appDetails.Workspace.ID, codersdk.UpsertWorkspaceAgentPortShareRequest{
				AgentName:  proxyTestAgentName,
				Port:       int32(port),
				ShareLevel: codersdk.WorkspaceAgentPortShareLevelPublic,
				Protocol:   codersdk.WorkspaceAgentPortShareProtocolHTTP,
			})
			require.NoError(t, err)

			publicAppClient := appDetails.AppClient(t)
			publicAppClient.SetSessionToken("")

			resp, err := requestWithRetries(ctx, t, publicAppClient, http.MethodGet, appDetails.SubdomainAppURL(appDetails.Apps.Port).String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			assertWorkspaceLastUsedAtUpdated(t, appDetails)
		})

		t.Run("HTTPS", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			appDetails := setupProxyTest(t, &DeploymentOptions{
				ServeHTTPS: true,
			})
			// using the fact that Apps.Port and Apps.PortHTTPS are the same port here
			port, err := strconv.ParseInt(appDetails.Apps.Port.AppSlugOrPort, 10, 32)
			require.NoError(t, err)
			_, err = appDetails.SDKClient.UpsertWorkspaceAgentPortShare(ctx, appDetails.Workspace.ID, codersdk.UpsertWorkspaceAgentPortShareRequest{
				AgentName:  proxyTestAgentName,
				Port:       int32(port),
				ShareLevel: codersdk.WorkspaceAgentPortShareLevelPublic,
				Protocol:   codersdk.WorkspaceAgentPortShareProtocolHTTPS,
			})
			require.NoError(t, err)

			publicAppClient := appDetails.AppClient(t)
			publicAppClient.SetSessionToken("")

			resp, err := requestWithRetries(ctx, t, publicAppClient, http.MethodGet, appDetails.SubdomainAppURL(appDetails.Apps.PortHTTPS).String(), nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			assertWorkspaceLastUsedAtUpdated(t, appDetails)
		})
	})

	t.Run("CORS", func(t *testing.T) {
		t.Parallel()

		// Set up test headers that should be returned by the app
		testHeaders := http.Header{
			"Access-Control-Allow-Origin":  []string{"*"},
			"Access-Control-Allow-Methods": []string{"GET, POST, OPTIONS"},
		}

		unauthenticatedClient := func(t *testing.T, appDetails *Details) *codersdk.Client {
			c := appDetails.AppClient(t)
			c.SetSessionToken("")
			return c
		}

		authenticatedClient := func(t *testing.T, appDetails *Details) *codersdk.Client {
			uc, _ := coderdtest.CreateAnotherUser(t, appDetails.SDKClient, appDetails.FirstUser.OrganizationID, rbac.RoleMember())
			c := appDetails.AppClient(t)
			c.SetSessionToken(uc.SessionToken())
			return c
		}

		ownerClient := func(t *testing.T, appDetails *Details) *codersdk.Client {
			c := appDetails.AppClient(t)                           // <-- Use same server as others
			c.SetSessionToken(appDetails.SDKClient.SessionToken()) // But with owner auth
			return c
		}

		tests := []struct {
			name                string
			shareLevel          codersdk.WorkspaceAgentPortShareLevel
			behavior            codersdk.CORSBehavior
			client              func(t *testing.T, appDetails *Details) *codersdk.Client
			expectedStatusCode  int
			expectedCORSHeaders bool
		}{
			// Public
			{
				name:                "Default/Public",
				shareLevel:          codersdk.WorkspaceAgentPortShareLevelPublic,
				behavior:            codersdk.CORSBehaviorSimple,
				expectedCORSHeaders: false,
				client:              unauthenticatedClient,
				expectedStatusCode:  http.StatusOK,
			},
			{ // fails
				name:                "Passthru/Public",
				shareLevel:          codersdk.WorkspaceAgentPortShareLevelPublic,
				behavior:            codersdk.CORSBehaviorPassthru,
				expectedCORSHeaders: true,
				client:              unauthenticatedClient,
				expectedStatusCode:  http.StatusOK,
			},
			// Authenticated
			{
				name:                "Default/Authenticated",
				shareLevel:          codersdk.WorkspaceAgentPortShareLevelAuthenticated,
				behavior:            codersdk.CORSBehaviorSimple,
				expectedCORSHeaders: false,
				client:              authenticatedClient,
				expectedStatusCode:  http.StatusOK,
			},
			{
				name:                "Passthru/Authenticated",
				shareLevel:          codersdk.WorkspaceAgentPortShareLevelAuthenticated,
				behavior:            codersdk.CORSBehaviorPassthru,
				expectedCORSHeaders: true,
				client:              authenticatedClient,
				expectedStatusCode:  http.StatusOK,
			},
			{
				// The CORS behavior will not affect unauthenticated requests.
				// The request will be redirected to the login page.
				name:                "Passthru/Unauthenticated",
				shareLevel:          codersdk.WorkspaceAgentPortShareLevelAuthenticated,
				behavior:            codersdk.CORSBehaviorPassthru,
				expectedCORSHeaders: false,
				client:              unauthenticatedClient,
				expectedStatusCode:  http.StatusSeeOther,
			},
			// Owner
			{
				name:                "Default/Owner",
				shareLevel:          codersdk.WorkspaceAgentPortShareLevelAuthenticated, // Owner is not a valid share level for ports.
				behavior:            codersdk.CORSBehaviorSimple,
				expectedCORSHeaders: false,
				client:              ownerClient,
				expectedStatusCode:  http.StatusOK,
			},
			{ // fails
				name:                "Passthru/Owner",
				shareLevel:          codersdk.WorkspaceAgentPortShareLevelAuthenticated, // Owner is not a valid share level for ports.
				behavior:            codersdk.CORSBehaviorPassthru,
				expectedCORSHeaders: true,
				client:              ownerClient,
				expectedStatusCode:  http.StatusOK,
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
				defer cancel()

				appDetails := setupProxyTest(t, &DeploymentOptions{
					headers: testHeaders,
				})
				port, err := strconv.ParseInt(appDetails.Apps.Port.AppSlugOrPort, 10, 32)
				require.NoError(t, err)

				// Update the template CORS behavior.
				b := tc.behavior
				template, err := appDetails.SDKClient.UpdateTemplateMeta(ctx, appDetails.Workspace.TemplateID, codersdk.UpdateTemplateMeta{
					CORSBehavior: &b,
				})
				require.NoError(t, err)
				require.Equal(t, tc.behavior, template.CORSBehavior)

				// Set the port we have to be shared.
				_, err = appDetails.SDKClient.UpsertWorkspaceAgentPortShare(ctx, appDetails.Workspace.ID, codersdk.UpsertWorkspaceAgentPortShareRequest{
					AgentName:  proxyTestAgentName,
					Port:       int32(port),
					ShareLevel: tc.shareLevel,
					Protocol:   codersdk.WorkspaceAgentPortShareProtocolHTTP,
				})
				require.NoError(t, err)

				client := tc.client(t, appDetails)

				resp, err := requestWithRetries(ctx, t, client, http.MethodGet, appDetails.SubdomainAppURL(appDetails.Apps.Port).String(), nil)
				require.NoError(t, err)
				defer resp.Body.Close()
				require.Equal(t, tc.expectedStatusCode, resp.StatusCode)

				if tc.expectedCORSHeaders {
					require.Equal(t, testHeaders.Get("Access-Control-Allow-Origin"), resp.Header.Get("Access-Control-Allow-Origin"), "allow origin did not match")
					require.Equal(t, testHeaders.Get("Access-Control-Allow-Methods"), resp.Header.Get("Access-Control-Allow-Methods"), "allow methods did not match")
				} else {
					require.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
					require.Empty(t, resp.Header.Get("Access-Control-Allow-Methods"))
				}
			})
		}
	})

	t.Run("AppSharing", func(t *testing.T) {
		t.Parallel()

		setup := func(t *testing.T, allowPathAppSharing, allowSiteOwnerAccess bool) (appDetails *Details, workspace codersdk.Workspace, agnt codersdk.WorkspaceAgent, user codersdk.User, ownerClient *codersdk.Client, client *codersdk.Client, clientInOtherOrg *codersdk.Client, clientWithNoAuth *codersdk.Client) {
			//nolint:gosec
			const password = "SomeSecurePassword!"

			appDetails = setupProxyTest(t, &DeploymentOptions{
				DangerousAllowPathAppSharing:         allowPathAppSharing,
				DangerousAllowPathAppSiteOwnerAccess: allowSiteOwnerAccess,
				// we make the workspace below
				noWorkspace: true,
			})

			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			t.Cleanup(cancel)

			// Create a template-admin user in the same org. We don't use an owner
			// since they have access to everything.
			ownerClient = appDetails.SDKClient
			user, err := ownerClient.CreateUserWithOrgs(ctx, codersdk.CreateUserRequestWithOrgs{
				Email:           "user@coder.com",
				Username:        "user",
				Password:        password,
				OrganizationIDs: []uuid.UUID{appDetails.FirstUser.OrganizationID},
			})
			require.NoError(t, err)

			_, err = ownerClient.UpdateUserRoles(ctx, user.ID.String(), codersdk.UpdateRoles{
				Roles: []string{"template-admin", "member"},
			})
			require.NoError(t, err)

			client = codersdk.New(ownerClient.URL)
			loginRes, err := client.LoginWithPassword(ctx, codersdk.LoginWithPasswordRequest{
				Email:    user.Email,
				Password: password,
			})
			require.NoError(t, err)
			client.SetSessionToken(loginRes.SessionToken)
			client.HTTPClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			}
			forceURLTransport(t, client)

			// Create workspace.
			port := appServer(t, nil, false, nil)
			workspace, _ = createWorkspaceWithApps(t, client, user.OrganizationIDs[0], user, port, false)

			// Verify that the apps have the correct sharing levels set.
			workspaceBuild, err := client.WorkspaceBuild(ctx, workspace.LatestBuild.ID)
			require.NoError(t, err)
			require.NotEmpty(t, workspaceBuild.Resources, "workspace build has no resources")
			require.NotEmpty(t, workspaceBuild.Resources[0].Agents, "workspace build has no agents")
			agnt = workspaceBuild.Resources[0].Agents[0]
			found := map[string]codersdk.WorkspaceAppSharingLevel{}
			expected := map[string]codersdk.WorkspaceAppSharingLevel{
				proxyTestAppNameFake:                      codersdk.WorkspaceAppSharingLevelOwner,
				proxyTestAppNameOwner:                     codersdk.WorkspaceAppSharingLevelOwner,
				proxyTestAppNameAuthenticated:             codersdk.WorkspaceAppSharingLevelAuthenticated,
				proxyTestAppNamePublic:                    codersdk.WorkspaceAppSharingLevelPublic,
				proxyTestAppNameAuthenticatedCORSPassthru: codersdk.WorkspaceAppSharingLevelAuthenticated,
				proxyTestAppNamePublicCORSPassthru:        codersdk.WorkspaceAppSharingLevelPublic,
				proxyTestAppNameAuthenticatedCORSDefault:  codersdk.WorkspaceAppSharingLevelAuthenticated,
				proxyTestAppNamePublicCORSDefault:         codersdk.WorkspaceAppSharingLevelPublic,
			}
			for _, app := range agnt.Apps {
				found[app.DisplayName] = app.SharingLevel
			}
			require.Equal(t, expected, found, "apps have incorrect sharing levels")

			// Create a user in a different org.
			otherOrg, err := ownerClient.CreateOrganization(ctx, codersdk.CreateOrganizationRequest{
				Name: "a-different-org",
			})
			require.NoError(t, err)
			userInOtherOrg, err := ownerClient.CreateUserWithOrgs(ctx, codersdk.CreateUserRequestWithOrgs{
				Email:           "no-template-access@coder.com",
				Username:        "no-template-access",
				Password:        password,
				OrganizationIDs: []uuid.UUID{otherOrg.ID},
			})
			require.NoError(t, err)

			clientInOtherOrg = codersdk.New(client.URL)
			loginRes, err = clientInOtherOrg.LoginWithPassword(ctx, codersdk.LoginWithPasswordRequest{
				Email:    userInOtherOrg.Email,
				Password: password,
			})
			require.NoError(t, err)
			clientInOtherOrg.SetSessionToken(loginRes.SessionToken)
			clientInOtherOrg.HTTPClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			}
			forceURLTransport(t, clientInOtherOrg)

			// Create an unauthenticated codersdk client.
			clientWithNoAuth = codersdk.New(client.URL)
			clientWithNoAuth.HTTPClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			}
			forceURLTransport(t, clientWithNoAuth)

			return appDetails, workspace, agnt, user, ownerClient, client, clientInOtherOrg, clientWithNoAuth
		}

		verifyAccess := func(t *testing.T, appDetails *Details, isPathApp bool, username, workspaceName, agentName, appName string, client *codersdk.Client, shouldHaveAccess, shouldRedirectToLogin bool) {
			t.Helper()

			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			// If the client has a session token, we also want to check that a
			// scoped key works.
			sessionTokens := []string{client.SessionToken()}
			if client.SessionToken() != "" {
				token, err := client.CreateToken(ctx, codersdk.Me, codersdk.CreateTokenRequest{
					Scope: codersdk.APIKeyScopeApplicationConnect,
				})
				require.NoError(t, err)

				sessionTokens = append(sessionTokens, token.Key)
			}

			for i, sessionToken := range sessionTokens {
				msg := fmt.Sprintf("client %d", i)

				app := App{
					Username:      username,
					WorkspaceName: workspaceName,
					AgentName:     agentName,
					AppSlugOrPort: appName,
					Query:         proxyTestAppQuery,
				}
				u := appDetails.SubdomainAppURL(app)
				if isPathApp {
					u = appDetails.PathAppURL(app)
				}

				client := appDetails.AppClient(t)
				client.SetSessionToken(sessionToken)
				res, err := requestWithRetries(ctx, t, client, http.MethodGet, u.String(), nil)
				require.NoError(t, err, msg)

				dump, err := httputil.DumpResponse(res, true)
				_ = res.Body.Close()
				require.NoError(t, err, msg)
				t.Log(u)
				t.Logf("response dump: %s", dump)

				if !shouldHaveAccess {
					if shouldRedirectToLogin {
						assert.Equal(t, http.StatusSeeOther, res.StatusCode, "should not have access, expected See Other redirect. "+msg)
						location, err := res.Location()
						require.NoError(t, err, msg)

						expectedPath := "/login"
						if !isPathApp || !appHostIsPrimary {
							expectedPath = "/api/v2/applications/auth-redirect"
						}
						assert.Equal(t, expectedPath, location.Path, "should not have access, expected redirect to applicable login endpoint. "+msg)
					} else {
						// If the user doesn't have access we return 404 to avoid
						// leaking information about the existence of the app.
						assert.Equal(t, http.StatusNotFound, res.StatusCode, "should not have access, expected not found. "+msg)
					}
				}

				if shouldHaveAccess {
					assert.Equal(t, http.StatusOK, res.StatusCode, "should have access, expected ok. "+msg)
					assert.Contains(t, string(dump), "hello world", "should have access, expected hello world. "+msg)
				}
			}
		}

		testLevels := func(t *testing.T, isPathApp, pathAppSharingEnabled, siteOwnerPathAppAccessEnabled bool) {
			appDetails, workspace, agnt, user, ownerClient, client, clientInOtherOrg, clientWithNoAuth := setup(t, pathAppSharingEnabled, siteOwnerPathAppAccessEnabled)

			allowedUnlessSharingDisabled := !isPathApp || pathAppSharingEnabled
			siteOwnerCanAccess := !isPathApp || siteOwnerPathAppAccessEnabled
			siteOwnerCanAccessShared := siteOwnerCanAccess || pathAppSharingEnabled

			deploymentConfig, err := ownerClient.DeploymentConfig(context.Background())
			require.NoError(t, err)

			assert.Equal(t, pathAppSharingEnabled, deploymentConfig.Values.Dangerous.AllowPathAppSharing.Value())
			assert.Equal(t, siteOwnerPathAppAccessEnabled, deploymentConfig.Values.Dangerous.AllowPathAppSiteOwnerAccess.Value())

			t.Run("LevelOwner", func(t *testing.T) {
				t.Parallel()

				// Site owner should be able to access all workspaces if
				// enabled.
				verifyAccess(t, appDetails, isPathApp, user.Username, workspace.Name, agnt.Name, proxyTestAppNameOwner, ownerClient, siteOwnerCanAccess, false)

				// Owner should be able to access their own workspace.
				verifyAccess(t, appDetails, isPathApp, user.Username, workspace.Name, agnt.Name, proxyTestAppNameOwner, client, true, false)

				// Authenticated users should not have access to a workspace that
				// they do not own.
				verifyAccess(t, appDetails, isPathApp, user.Username, workspace.Name, agnt.Name, proxyTestAppNameOwner, clientInOtherOrg, false, false)

				// Unauthenticated user should not have any access.
				verifyAccess(t, appDetails, isPathApp, user.Username, workspace.Name, agnt.Name, proxyTestAppNameOwner, clientWithNoAuth, false, true)
			})

			t.Run("LevelAuthenticated", func(t *testing.T) {
				t.Parallel()

				// Site owner should be able to access all workspaces if
				// enabled.
				verifyAccess(t, appDetails, isPathApp, user.Username, workspace.Name, agnt.Name, proxyTestAppNameAuthenticated, ownerClient, siteOwnerCanAccessShared, false)

				// Owner should be able to access their own workspace.
				verifyAccess(t, appDetails, isPathApp, user.Username, workspace.Name, agnt.Name, proxyTestAppNameAuthenticated, client, true, false)

				// Authenticated users should be able to access the workspace.
				verifyAccess(t, appDetails, isPathApp, user.Username, workspace.Name, agnt.Name, proxyTestAppNameAuthenticated, clientInOtherOrg, allowedUnlessSharingDisabled, false)

				// Unauthenticated user should not have any access.
				verifyAccess(t, appDetails, isPathApp, user.Username, workspace.Name, agnt.Name, proxyTestAppNameAuthenticated, clientWithNoAuth, false, true)
			})

			t.Run("LevelPublic", func(t *testing.T) {
				t.Parallel()

				// Site owner should be able to access all workspaces if
				// enabled.
				verifyAccess(t, appDetails, isPathApp, user.Username, workspace.Name, agnt.Name, proxyTestAppNamePublic, ownerClient, siteOwnerCanAccessShared, false)

				// Owner should be able to access their own workspace.
				verifyAccess(t, appDetails, isPathApp, user.Username, workspace.Name, agnt.Name, proxyTestAppNamePublic, client, true, false)

				// Authenticated users should be able to access the workspace.
				verifyAccess(t, appDetails, isPathApp, user.Username, workspace.Name, agnt.Name, proxyTestAppNamePublic, clientInOtherOrg, allowedUnlessSharingDisabled, false)

				// Unauthenticated user should be able to access the workspace.
				verifyAccess(t, appDetails, isPathApp, user.Username, workspace.Name, agnt.Name, proxyTestAppNamePublic, clientWithNoAuth, allowedUnlessSharingDisabled, !allowedUnlessSharingDisabled)
			})
		}

		t.Run("Path", func(t *testing.T) {
			t.Parallel()

			t.Run("Default", func(t *testing.T) {
				t.Parallel()
				testLevels(t, true, false, false)
			})

			t.Run("AppSharingEnabled", func(t *testing.T) {
				t.Parallel()
				testLevels(t, true, true, false)
			})

			t.Run("SiteOwnerAccessEnabled", func(t *testing.T) {
				t.Parallel()
				testLevels(t, true, false, true)
			})

			t.Run("BothEnabled", func(t *testing.T) {
				t.Parallel()
				testLevels(t, true, false, true)
			})
		})

		t.Run("Subdomain", func(t *testing.T) {
			t.Parallel()
			testLevels(t, false, false, false)
		})
	})

	t.Run("WorkspaceAppsNonCanonicalHeaders", func(t *testing.T) {
		t.Parallel()

		// Start a TCP server that manually parses the request. Golang's HTTP
		// server canonicalizes all HTTP request headers it receives, so we
		// can't use it to test that we forward non-canonical headers.
		// #nosec
		ln, err := net.Listen("tcp", ":0")
		require.NoError(t, err)
		go func() {
			for {
				c, err := ln.Accept()
				if xerrors.Is(err, net.ErrClosed) {
					return
				}
				require.NoError(t, err)

				go func() {
					s := bufio.NewScanner(c)

					// Read request line.
					assert.True(t, s.Scan())
					reqLine := s.Text()
					assert.True(t, strings.HasPrefix(reqLine, fmt.Sprintf("GET /?%s HTTP/1.1", proxyTestAppQuery)))

					// Read headers and discard them. We collect the
					// Sec-WebSocket-Key header (with a capital S) to respond
					// with.
					secWebSocketKey := "(none found)"
					for s.Scan() {
						if s.Text() == "" {
							break
						}

						line := strings.TrimSpace(s.Text())
						if strings.HasPrefix(line, "Sec-WebSocket-Key: ") {
							secWebSocketKey = strings.TrimPrefix(line, "Sec-WebSocket-Key: ")
						}
					}

					// Write response containing text/plain with the
					// Sec-WebSocket-Key header.
					res := fmt.Sprintf("HTTP/1.1 204 No Content\r\nSec-WebSocket-Key: %s\r\nConnection: close\r\n\r\n", secWebSocketKey)
					_, err = c.Write([]byte(res))
					assert.NoError(t, err)
					err = c.Close()
					assert.NoError(t, err)
				}()
			}
		}()
		t.Cleanup(func() {
			_ = ln.Close()
		})
		tcpAddr, ok := ln.Addr().(*net.TCPAddr)
		require.True(t, ok)

		appDetails := setupProxyTest(t, &DeploymentOptions{
			// #nosec G115 - Safe conversion as TCP port numbers are within uint16 range (0-65535)
			port: uint16(tcpAddr.Port),
		})

		cases := []struct {
			name string
			u    *url.URL
		}{
			{
				name: "ProxyPath",
				u:    appDetails.PathAppURL(appDetails.Apps.Owner),
			},
			{
				name: "ProxySubdomain",
				u:    appDetails.SubdomainAppURL(appDetails.Apps.Owner),
			},
		}

		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()

				ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
				defer cancel()

				req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.u.String(), nil)
				require.NoError(t, err)

				// Use a non-canonical header name. The S in Sec-WebSocket-Key should be
				// capitalized according to the websocket spec, but Golang will
				// lowercase it to match the HTTP/1 spec.
				//
				// Setting the header on the map directly will force the header to not
				// be canonicalized on the client, but it will be canonicalized on the
				// server.
				secWebSocketKey := "test-dean-was-here"
				req.Header["Sec-WebSocket-Key"] = []string{secWebSocketKey}
				req.Header.Set(codersdk.SessionTokenHeader, appDetails.SDKClient.SessionToken())

				resp, err := doWithRetries(t, appDetails.AppClient(t), req)
				require.NoError(t, err)
				defer resp.Body.Close()

				// The response should be a 204 No Content with the Sec-WebSocket-Key
				// header set to the value we sent.
				res, err := httputil.DumpResponse(resp, true)
				require.NoError(t, err)
				t.Log(string(res))
				require.Equal(t, http.StatusNoContent, resp.StatusCode)
				require.Equal(t, secWebSocketKey, resp.Header.Get("Sec-WebSocket-Key"))
			})
		}
	})

	t.Run("CORSHeadersStripped", func(t *testing.T) {
		t.Parallel()

		appDetails := setupProxyTest(t, &DeploymentOptions{
			headers: http.Header{
				"X-Foobar":                         []string{"baz"},
				"Access-Control-Allow-Origin":      []string{"http://localhost"},
				"access-control-allow-origin":      []string{"http://localhost"},
				"Access-Control-Allow-Credentials": []string{"true"},
				"Access-Control-Allow-Methods":     []string{"PUT"},
				"Access-Control-Allow-Headers":     []string{"X-Foobar"},
				"Vary": []string{
					"Origin",
					"origin",
					"Access-Control-Request-Headers",
					"access-Control-request-Headers",
					"Access-Control-Request-Methods",
					"ACCESS-CONTROL-REQUEST-METHODS",
					"X-Foobar",
				},
			},
		})

		appURL := appDetails.SubdomainAppURL(appDetails.Apps.Owner)

		ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
		defer cancel()

		resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, appURL.String(), nil)
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, []string(nil), resp.Header.Values("Access-Control-Allow-Origin"))
		require.Equal(t, []string(nil), resp.Header.Values("Access-Control-Allow-Credentials"))
		require.Equal(t, []string(nil), resp.Header.Values("Access-Control-Allow-Methods"))
		require.Equal(t, []string(nil), resp.Header.Values("Access-Control-Allow-Headers"))
		// Somehow there are two "Origin"s in Vary even though there should only be
		// one (from the CORS middleware), even if you remove the headers being sent
		// above.  When I do nothing else but change the expected value below to
		// have two "Origin"s suddenly Vary only has one.  It is somehow always the
		// opposite of whatever I put for the expected.  So, reluctantly, remove
		// duplicate "Origin" values.
		var deduped []string
		var addedOrigin bool
		for _, value := range resp.Header.Values("Vary") {
			if value != "Origin" || !addedOrigin {
				if value == "Origin" {
					addedOrigin = true
				}
				deduped = append(deduped, value)
			}
		}
		require.Equal(t, []string{"Origin", "X-Foobar"}, deduped)
		require.Equal(t, []string{"baz"}, resp.Header.Values("X-Foobar"))
	})

	t.Run("ReportStats", func(t *testing.T) {
		t.Parallel()

		reporter := &fakeStatsReporter{}
		appDetails := setupProxyTest(t, &DeploymentOptions{
			StatsCollectorOptions: workspaceapps.StatsCollectorOptions{
				Reporter:       reporter,
				ReportInterval: time.Hour,
				RollupWindow:   time.Minute,
			},
		})

		ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
		defer cancel()

		u := appDetails.PathAppURL(appDetails.Apps.Owner)
		resp, err := requestWithRetries(ctx, t, appDetails.AppClient(t), http.MethodGet, u.String(), nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		_, err = io.Copy(io.Discard, resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var stats []workspaceapps.StatsReport
		require.Eventually(t, func() bool {
			// Keep flushing until we get a non-empty stats report.
			appDetails.FlushStats()
			stats = reporter.stats()
			return len(stats) > 0
		}, testutil.WaitLong, testutil.IntervalFast, "stats not reported")

		assert.Equal(t, workspaceapps.AccessMethodPath, stats[0].AccessMethod)
		assert.Equal(t, "test-app-owner", stats[0].SlugOrPort)
		assert.Equal(t, 1, stats[0].Requests)
	})

	t.Run("WorkspaceOffline", func(t *testing.T) {
		t.Parallel()

		appDetails := setupProxyTest(t, nil)

		ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
		defer cancel()

		_ = coderdtest.MustTransitionWorkspace(t, appDetails.SDKClient, appDetails.Workspace.ID, codersdk.WorkspaceTransitionStart, codersdk.WorkspaceTransitionStop)

		u := appDetails.PathAppURL(appDetails.Apps.Owner)
		resp, err := appDetails.AppClient(t).Request(ctx, http.MethodGet, u.String(), nil)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"))
	})
}

type fakeStatsReporter struct {
	mu sync.Mutex
	s  []workspaceapps.StatsReport
}

func (r *fakeStatsReporter) stats() []workspaceapps.StatsReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.s
}

func (r *fakeStatsReporter) ReportAppStats(_ context.Context, stats []workspaceapps.StatsReport) error {
	r.mu.Lock()
	r.s = append(r.s, stats...)
	r.mu.Unlock()
	return nil
}

func testReconnectingPTY(ctx context.Context, t *testing.T, client *codersdk.Client, agentID uuid.UUID, signedToken string) {
	opts := workspacesdk.WorkspaceAgentReconnectingPTYOpts{
		AgentID:   agentID,
		Reconnect: uuid.New(),
		Width:     80,
		Height:    80,
		// --norc disables executing .bashrc, which is often used to customize the bash prompt
		Command:     "bash --norc",
		SignedToken: signedToken,
	}
	matchPrompt := func(line string) bool {
		return strings.Contains(line, "$ ") || strings.Contains(line, "# ")
	}
	matchEchoCommand := func(line string) bool {
		return strings.Contains(line, "echo test")
	}
	matchEchoOutput := func(line string) bool {
		return strings.Contains(line, "test") && !strings.Contains(line, "echo")
	}
	matchExitCommand := func(line string) bool {
		return strings.Contains(line, "exit")
	}
	matchExitOutput := func(line string) bool {
		return strings.Contains(line, "exit") || strings.Contains(line, "logout")
	}

	conn, err := workspacesdk.New(client).AgentReconnectingPTY(ctx, opts)
	require.NoError(t, err)
	defer conn.Close()

	tr := testutil.NewTerminalReader(t, conn)
	// Wait for the prompt before writing commands.  If the command arrives before the prompt is written, screen
	// will sometimes put the command output on the same line as the command and the test will flake
	require.NoError(t, tr.ReadUntil(ctx, matchPrompt), "find prompt")

	data, err := json.Marshal(workspacesdk.ReconnectingPTYRequest{
		Data: "echo test\r",
	})
	require.NoError(t, err)
	_, err = conn.Write(data)
	require.NoError(t, err)

	require.NoError(t, tr.ReadUntil(ctx, matchEchoCommand), "find echo command")
	require.NoError(t, tr.ReadUntil(ctx, matchEchoOutput), "find echo output")

	// Exit should cause the connection to close.
	data, err = json.Marshal(workspacesdk.ReconnectingPTYRequest{
		Data: "exit\r",
	})
	require.NoError(t, err)
	_, err = conn.Write(data)
	require.NoError(t, err)

	// Once for the input and again for the output.
	require.NoError(t, tr.ReadUntil(ctx, matchExitCommand), "find exit command")
	require.NoError(t, tr.ReadUntil(ctx, matchExitOutput), "find exit output")

	// Ensure the connection closes.
	require.ErrorIs(t, tr.ReadUntil(ctx, nil), io.EOF)
}

// Accessing an app should update the workspace's LastUsedAt.
// NOTE: Despite our efforts with the flush channel, this is inherently racy when used with
// parallel tests on the same workspace/app.
func assertWorkspaceLastUsedAtUpdated(t testing.TB, details *Details) {
	t.Helper()

	require.NotNil(t, details.Workspace, "can't assert LastUsedAt on a nil workspace!")
	before, err := details.SDKClient.Workspace(context.Background(), details.Workspace.ID)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		// We may need to flush multiple times, since the stats from the app we are testing might be
		// collected asynchronously from when we see the connection close, and thus, could race
		// against being flushed.
		details.FlushStats()
		after, err := details.SDKClient.Workspace(context.Background(), details.Workspace.ID)
		return assert.NoError(t, err) && after.LastUsedAt.After(before.LastUsedAt)
	}, testutil.WaitShort, testutil.IntervalMedium)
}

// Except when it sometimes shouldn't (e.g. no access)
// NOTE: Despite our efforts with the flush channel, this is inherently racy when used with
// parallel tests on the same workspace/app.
func assertWorkspaceLastUsedAtNotUpdated(t testing.TB, details *Details) {
	t.Helper()

	require.NotNil(t, details.Workspace, "can't assert LastUsedAt on a nil workspace!")
	before, err := details.SDKClient.Workspace(context.Background(), details.Workspace.ID)
	require.NoError(t, err)
	details.FlushStats()
	after, err := details.SDKClient.Workspace(context.Background(), details.Workspace.ID)
	require.NoError(t, err)
	require.Equal(t, before.LastUsedAt, after.LastUsedAt, "workspace LastUsedAt updated when it should not have been")
}

func generateBadJWE(t *testing.T, claims interface{}) string {
	t.Helper()
	var buf [32]byte
	_, err := rand.Read(buf[:])
	require.NoError(t, err)
	encrypt, err := jose.NewEncrypter(
		jose.A256GCM,
		jose.Recipient{
			Algorithm: jose.A256GCMKW,
			Key:       buf[:],
		}, &jose.EncrypterOptions{
			Compression: jose.DEFLATE,
		},
	)
	require.NoError(t, err)
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	signed, err := encrypt.Encrypt(payload)
	require.NoError(t, err)
	compact, err := signed.CompactSerialize()
	require.NoError(t, err)
	return compact
}

// generateBadJWT generates a JWT with a random key. It's intended to emulate the old-style JWT's we generated.
func generateBadJWT(t *testing.T, claims interface{}) string {
	t.Helper()

	var buf [64]byte
	_, err := rand.Read(buf[:])
	require.NoError(t, err)
	signer, err := jose.NewSigner(jose.SigningKey{
		Algorithm: jose.HS512,
		Key:       buf[:],
	}, nil)
	require.NoError(t, err)
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	signed, err := signer.Sign(payload)
	require.NoError(t, err)
	compact, err := signed.CompactSerialize()
	require.NoError(t, err)
	return compact
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}
