package middleware

import "net/http"


// SecurityHeaders is an HTTP middleware that adds a standard set of
// security-related headers to every response. These headers reduce
// exposure to common web vulnerabilities and enforce safer browser
// behavior when the application is accessed through a browser.
//
// Applied headers:
//
// - X-Content-Type-Options: "nosniff"
//   Instructs browsers not to perform MIME-type sniffing. Prevents
//   content from being interpreted as a different type than declared,
//   reducing exposure to drive-by download and content injection attacks.
//
// - Cache-Control: "no-store, no-cache, must-revalidate"
//   Disables caching of responses to ensure sensitive data (e.g., metrics,
//   APIs) is never stored by browsers or intermediate proxies.
//
// - Pragma: "no-cache"
//   Legacy HTTP/1.0 directive complementing Cache-Control for
//   backward compatibility.
//
// - Cross-Origin-Opener-Policy: "same-origin"
//   Isolates the browsing context from cross-origin windows, reducing
//   risks of side-channel attacks such as Spectre.
//
// - Cross-Origin-Resource-Policy: "same-origin"
//   Restricts other origins from embedding these responses, mitigating
//   unauthorized data leaks through resource inclusion.
//
// - X-XSS-Protection: "1; mode=block"
//   Legacy protection for older browsers. Instructs the browser to block
//   rendering if a reflected XSS attack is detected.
//
// - Content-Security-Policy: "default-src 'self'"
//   Restricts all resource loading to the same origin by default. While
//   mainly relevant for web pages, it provides defense-in-depth even for
//   API responses if viewed in a browser.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Content-Security-Policy", "default-src 'self'")
		next.ServeHTTP(w, r)
	})
}
