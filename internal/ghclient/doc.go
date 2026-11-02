// Package ghclient is the only package in pr-relay that touches the network
// or a GitHub token.
//
// # Token resolution (DMN-03)
//
// TokenSource reuses the user's existing gh login with no extra setup, in
// the same order as gh itself:
//
//  1. GH_TOKEN, then GITHUB_TOKEN (GH_ENTERPRISE_TOKEN for other hosts)
//  2. oauth_token in gh's hosts.yml (GH_CONFIG_DIR selects the directory)
//  3. `gh auth token --secure-storage --hostname <host>` as the keyring
//     fallback, located through TokenSource.GhPath, GH_PATH or PATH
//
// Every resolution first calls go-gh's config.Reload, because config.Read is
// cached for the life of the process and a `gh auth login` rewrites
// hosts.yml under a running daemon. The keyring fallback runs with a bounded
// timeout (10s by default), so a locked or prompting keyring can never hang
// the poller.
//
// # Reload on 401
//
// Client caches the resolved token. A 401 drops the cache, re-resolves the
// token (reloading hosts.yml) and retries the batch exactly once. A second
// 401 returns ErrUnauthorized.
//
// # Secrets
//
// The token lives only in memory. It is never logged, persisted, or placed
// in an error string, and response headers and bodies are never logged or
// placed in errors either. Only the token's source (GH_TOKEN, GITHUB_TOKEN,
// oauth_token, gh) is reported.
//
// # Endpoint
//
// The token is sent only to DefaultEndpoint (https://api.github.com/graphql).
// New accepts another endpoint only when its host is loopback (127.0.0.1,
// ::1 or localhost), which tests use; anything else is rejected so a
// misconfigured override cannot exfiltrate the token.
//
// # Error taxonomy (pitfall 10)
//
// Batch classifies every failed response into one typed error:
//
//   - 401, twice: ErrUnauthorized
//   - secondary limit (429, a Retry-After header on 200/403/429, or a
//     "secondary rate limit" message): *SecondaryLimitError
//   - primary exhaustion (x-ratelimit-remaining: 0 on a failed response, or
//     a RATE_LIMITED GraphQL error): *PrimaryExhaustedError
//   - 5xx, timeouts and other transport failures: *TransientError
//   - any other 403: ErrForbidden
//
// The scheduler maps these onto the D-07 backoff.
package ghclient
