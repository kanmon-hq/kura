## 2024-05-19 - Hardcoded Internal Secret Key
**Vulnerability:** Hardcoded internal secret key (`internalSecretKey = "itcp_internal_service_secret_key_888"`) found in `internal/delivery/http/api.go` used to bypass admin authentication.
**Learning:** Hardcoding secrets like internal API keys in the source code provides a critical vulnerability where an attacker with source code access or binary extraction could easily bypass authentication controls, essentially getting full administrative access.
**Prevention:** Avoid hardcoding any secrets in the codebase. Always use environment variables (e.g. `os.Getenv`) and inject these secrets into the application via configuration structures.
## 2025-02-28 - [Timing Attack Mitigation for Secrets]
**Vulnerability:** String comparison (`==`) was being used for validating `adminAPIKey` and `internalSecret`, which can expose the application to timing attacks where an attacker can guess the secret character by character based on response times.
**Learning:** In Go, string comparison terminates at the first mismatched character. This is a common pattern for standard strings but insecure for secrets.
**Prevention:** Always use `crypto/subtle.ConstantTimeCompare` when comparing tokens, API keys, or any secret data. Convert strings to `[]byte` before comparison.
## 2025-02-28 - [Fail Securely on Missing Authorization Component]
**Vulnerability:** The `verifyAdmin` function in `internal/delivery/http/api.go` was returning `true` when the `adminHandler` was `nil`. This is an authorization bypass, meaning any endpoints guarded by this function would allow unauthenticated access if the handler failed to initialize or was not properly passed into the API setup.
**Learning:** Returning `true` or bypassing checks when a component is missing violates the "fail securely" principle. If a security component (like an authorization handler) is missing, the application must default to denying access rather than allowing it.
**Prevention:** Always default to `false` or return an error in authorization and authentication checks, especially when dependencies are missing.
## 2025-02-28 - [Prevent Information Leakage in Proxy Errors]
**Vulnerability:** The reverse proxy was exposing detailed underlying network or vendor errors (such as TLS handshake failures, connection resets, or upstream timeouts) to the API client using `fmt.Sprintf` in the `sendError` function.
**Learning:** Exposing raw backend errors to the client can leak sensitive internal network topology, configuration details, or upstream dependencies, violating the "fail securely" and "defense in depth" principles.
**Prevention:** Always log detailed error information internally (including a `RequestID` for traceability) and return a safe, generic error message (e.g., "Vendor connection error") to the end user.
