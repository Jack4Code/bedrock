package bedrock

import "fmt"

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

	// Public routes live on the public hostname (e.g. api.mydomain.com)
	// with no edge gating. Open to the internet — apply application auth
	// (JWT, etc.) as needed via Middleware.
	Public

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
