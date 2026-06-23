package bedrock

import (
	"fmt"
	"strings"
)

// Visibility declares the network surface a Route is exposed on.
//
// Visibility is a deployment concern: it determines which hostname the route
// is registered under, and (paired with Traefik or your reverse proxy) which
// network gating applies. It is NOT application-layer authentication —
// that's still your job via per-route Middleware (e.g. RequireAuth for JWT,
// signature verification for webhooks).
//
// The zero value is Private, so a Route declared without a Visibility is
// safe by default: forgetting to set it produces a 404 on the public surface,
// not a leak.
type Visibility int

const (
	// Private routes live on the internal hostname (e.g.
	// api.internal.mydomain.com). The gate is your VPN and/or admin IP
	// allowlist, enforced by Traefik. Application-layer auth is optional —
	// for read-only introspection the network is usually enough; for
	// destructive admin actions, layer auth on top via Middleware.
	Private Visibility = iota

	// Gated routes live on the public hostname behind an edge allowlist
	// (e.g. Stripe IPs, Clerk IPs, monitoring service IPs, partner CIDRs).
	// The handler should always verify the request itself — signature
	// check, mTLS, HMAC — since IP allowlists drift and aren't a real
	// security boundary.
	//
	// Webhooks are the canonical example, but anything matching this
	// shape fits: monitoring callbacks, partner integrations, CDN origin
	// pulls, SSO assertions from a known IdP.
	Gated

	// Public routes live on the public hostname (e.g. api.mydomain.com)
	// with no edge gating. Open to the internet — apply application auth
	// (JWT, etc.) as needed via Middleware.
	Public
)

func (v Visibility) String() string {
	switch v {
	case Private:
		return "private"
	case Public:
		return "public"
	case Gated:
		return "gated"
	default:
		return fmt.Sprintf("visibility(%d)", int(v))
	}
}

// ParseVisibility is the inverse of String: it parses a visibility name
// ("public", "private" or "gated", case-insensitive, surrounding space
// ignored) into a Visibility. It is used to read the BEDROCK_SERVE env var.
func ParseVisibility(s string) (Visibility, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "private":
		return Private, nil
	case "gated":
		return Gated, nil
	case "public":
		return Public, nil
	default:
		return 0, fmt.Errorf("unknown visibility %q (want public, private or gated)", s)
	}
}

// serveSet is the set of visibilities a process registers routes for. A nil or
// empty set means "serve all visibilities" — the default, backward-compatible
// behaviour where one process serves every surface. A non-empty set restricts
// the process to the listed visibilities, so the same image can be deployed as
// separate task groups that each own a subset of the surface. See Options.Serve.
type serveSet map[Visibility]bool

// serves reports whether routes of the given visibility should be registered.
// An empty set serves everything.
func (s serveSet) serves(v Visibility) bool {
	if len(s) == 0 {
		return true
	}
	return s[v]
}

// String renders the set as a sorted, comma-separated list for logging, or
// "all" when the set is empty.
func (s serveSet) String() string {
	if len(s) == 0 {
		return "all"
	}
	names := make([]string, 0, len(s))
	for _, v := range []Visibility{Public, Gated, Private} {
		if s[v] {
			names = append(names, v.String())
		}
	}
	return strings.Join(names, ",")
}

// HostConfig maps each Visibility to the hostname it should match in the
// router. Set this in Options when calling RunWithOptions.
//
// Public and Gated typically share the same public hostname; the difference
// between them is enforced at the reverse proxy layer (no allowlist vs. an
// IP allowlist) and reinforced in your handler (signature verification for
// gated routes).
type HostConfig struct {
	Public  string // e.g. "api.mydomain.com"
	Private string // e.g. "api.internal.mydomain.com"
	Gated   string // e.g. "api.mydomain.com" (often same as Public)

	// TrustLoopback, when true, additionally registers ALL routes — regardless
	// of Visibility — on a subrouter that matches a loopback Host header
	// (localhost, 127.0.0.1 or ::1, with or without a port). It does not change
	// the per-visibility host matching for external traffic.
	//
	// This exists for co-located callers that reach the server directly,
	// bypassing the reverse proxy — e.g. a server-rendered frontend running on
	// the same host that calls http://localhost:<port>. Such a request carries
	// Host: localhost and would otherwise match no visibility subrouter (404).
	//
	// SECURITY: only enable this when the HTTP port is not reachable from
	// outside the host (firewalled to loopback). If the port is exposed, a
	// remote client could send Host: localhost and reach gated/private routes,
	// bypassing the edge allowlist. The reverse-proxy gating is the real
	// boundary; this flag deliberately trusts the loopback interface instead.
	TrustLoopback bool
}

// hostFor returns the hostname a route with the given Visibility should be
// registered under, or "" if no host is configured for that visibility.
func (h HostConfig) hostFor(v Visibility) string {
	switch v {
	case Public:
		return h.Public
	case Private:
		return h.Private
	case Gated:
		return h.Gated
	default:
		return h.Private // unknown -> safest default
	}
}

// configured reports whether any hostnames are set. If none are set, bedrock
// skips host matching entirely — useful for local dev where you hit the
// server directly without a reverse proxy in front.
func (h HostConfig) configured() bool {
	return h.Public != "" || h.Private != "" || h.Gated != ""
}
