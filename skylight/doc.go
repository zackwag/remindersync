// Package skylight is a client for the Skylight Calendar API at
// app.ourskylight.com.
//
// Skylight doesn't publish this API. Everything here was worked out from the
// Skylight web and mobile apps, largely by way of the MIT-licensed
// MegaTheLEGEND/skylight_calendar Home Assistant integration. Treat any field
// or endpoint marked "unverified" as a guess until a real response confirms it.
//
// Authentication is OAuth2 authorization code + PKCE against Skylight's own
// mobile client. See [OAuth] for the one-time login and [Client] for
// authenticated requests, which refresh the access token automatically.
package skylight
