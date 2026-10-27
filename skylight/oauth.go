package skylight

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the Skylight API host.
	DefaultBaseURL = "https://app.ourskylight.com"

	// RedirectURI is the only redirect URI Skylight's OAuth server accepts for
	// the mobile client. Nothing listens there; after login the user copies the
	// ?code= value out of the browser's address bar.
	RedirectURI = "https://ourskylight.com/welcome"

	clientID   = "skylight-mobile"
	scope      = "everything"
	apiVersion = "2026-05-01"
	deviceName = "remindersync"

	// Skylight rejects requests without a browser-like User-Agent.
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// ErrAuth is returned when Skylight rejects credentials, including when a
// refresh token is no longer valid. Recovering requires a new login.
var ErrAuth = errors.New("skylight: authentication failed")

// Token is an OAuth access/refresh token pair.
type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// OAuth performs Skylight's token exchanges. The zero value talks to
// [DefaultBaseURL] with [http.DefaultClient].
type OAuth struct {
	BaseURL    string
	HTTPClient *http.Client

	// DeviceFingerprint identifies this install to Skylight. Generate one with
	// [NewDeviceFingerprint] at login and reuse it for every refresh.
	DeviceFingerprint string
}

// PKCE is an RFC 7636 code verifier and its S256 challenge.
type PKCE struct {
	Verifier  string
	Challenge string
}

// NewPKCE generates a random PKCE verifier and challenge.
func NewPKCE() (PKCE, error) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return PKCE{}, fmt.Errorf("generate PKCE verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{Verifier: verifier, Challenge: base64.RawURLEncoding.EncodeToString(sum[:])}, nil
}

// NewDeviceFingerprint returns a random identifier for this install.
func NewDeviceFingerprint() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate device fingerprint: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// AuthorizeURL is the login page to open in a browser for the given PKCE
// challenge.
func (o OAuth) AuthorizeURL(challenge string) string {
	q := url.Values{
		"client_id":             {clientID},
		"response_type":         {"code"},
		"scope":                 {scope},
		"redirect_uri":          {RedirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"prompt":                {"login"},
	}
	return o.baseURL() + "/oauth/authorize?" + q.Encode()
}

// ExtractCode accepts either a bare authorization code or the full redirect
// URL the browser landed on, and returns the code.
func ExtractCode(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("no authorization code provided")
	}
	if strings.Contains(raw, "://") || strings.HasPrefix(raw, "/") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("parse redirect URL: %w", err)
		}
		code := u.Query().Get("code")
		if code == "" {
			return "", errors.New("redirect URL has no code parameter")
		}
		return code, nil
	}
	if strings.HasPrefix(strings.ToLower(raw), "code=") {
		return raw[len("code="):], nil
	}
	return raw, nil
}

// ExchangeCode trades an authorization code for tokens.
func (o OAuth) ExchangeCode(ctx context.Context, code, verifier string) (Token, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {clientID},
		"redirect_uri":  {RedirectURI},
		"code_verifier": {verifier},
		"scope":         {scope},
		// The app sends "js-mobile" on the code exchange but "web" on refresh.
		"source": {"js-mobile"},
	}
	o.addDeviceParams(form, "unknown")
	tok, err := o.tokenRequest(ctx, form)
	if err != nil {
		return Token{}, err
	}
	if tok.RefreshToken == "" {
		return Token{}, fmt.Errorf("%w: code exchange returned no refresh token", ErrAuth)
	}
	return tok, nil
}

// Refresh trades a refresh token for a new token pair. Skylight may or may
// not rotate the refresh token; when it doesn't, the old one is kept.
func (o OAuth) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
		"scope":         {scope},
		"source":        {"web"},
	}
	o.addDeviceParams(form, "10")
	tok, err := o.tokenRequest(ctx, form)
	if err != nil {
		return Token{}, err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return tok, nil
}

func (o OAuth) addDeviceParams(form url.Values, osVersion string) {
	form.Set("skylight_api_client_device_fingerprint", o.DeviceFingerprint)
	form.Set("skylight_api_client_device_platform", "web")
	form.Set("skylight_api_client_device_name", deviceName)
	form.Set("skylight_api_client_device_os_version", osVersion)
	form.Set("skylight_api_client_device_app_version", "unknown")
	form.Set("skylight_api_client_device_hardware", "3")
}

func (o OAuth) tokenRequest(ctx context.Context, form url.Values) (Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL()+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := o.httpClient().Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Token{}, fmt.Errorf("read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Token{}, fmt.Errorf("%w: token endpoint returned %d: %s", ErrAuth, resp.StatusCode, truncate(body, 200))
	}

	var tok Token
	if err := json.Unmarshal(body, &tok); err != nil {
		return Token{}, fmt.Errorf("decode token response: %w", err)
	}
	if tok.AccessToken == "" {
		return Token{}, fmt.Errorf("%w: token response has no access token", ErrAuth)
	}
	return tok, nil
}

func (o OAuth) baseURL() string {
	if o.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(o.BaseURL, "/")
}

func (o OAuth) httpClient() *http.Client {
	if o.HTTPClient == nil {
		return &http.Client{Timeout: 15 * time.Second}
	}
	return o.HTTPClient
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
