package googleworkspace

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	tokenEndpoint = "https://oauth2.googleapis.com/token"
	directoryScope = "https://www.googleapis.com/auth/admin.directory.user.readonly"
	apiBase        = "https://admin.googleapis.com/admin/directory/v1"
	jwtGrantType   = "urn:ietf:params:oauth:grant-type:jwt-bearer"
)

// serviceAccountKey mirrors the JSON key file downloaded from Google Cloud Console.
type serviceAccountKey struct {
	Type        string `json:"type"`
	ClientEmail string `json:"client_email"`
	ClientID    string `json:"client_id"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// User represents a Google Workspace directory user.
type User struct {
	ID           string   `json:"id"`
	PrimaryEmail string   `json:"primaryEmail"`
	Name         UserName `json:"name"`
	OrgUnitPath  string   `json:"orgUnitPath"`
	IsAdmin      bool     `json:"isAdmin"`
	Suspended    bool     `json:"suspended"`
	Archived     bool     `json:"archived"`
}

// UserName holds the name components returned by the Directory API.
type UserName struct {
	FullName   string `json:"fullName"`
	GivenName  string `json:"givenName"`
	FamilyName string `json:"familyName"`
}

// Client calls the Google Admin SDK Directory API using a service account
// with domain-wide delegation. Authentication is performed via a self-signed
// JWT exchanged for a short-lived access token — no external OAuth2 library
// is required.
type Client struct {
	adminEmail  string // Google Workspace super admin to impersonate
	domain      string
	ouPaths     []string // optional OU filter; empty = all users in domain
	privateKey  *rsa.PrivateKey
	clientEmail string // service account email (used as JWT iss claim)
	tokenURI    string // typically https://oauth2.googleapis.com/token
	httpClient  *http.Client

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

// NewClientFromFile creates a Client from a service account JSON key file.
// adminEmail must be a Google Workspace super admin. domain is the primary
// Workspace domain (e.g. "example.com"). ouPaths optionally restricts the
// user list to specific OUs and their subtrees; pass nil or an empty slice
// to sync all users in the domain.
func NewClientFromFile(credentialsFile, adminEmail, domain string, ouPaths []string) (*Client, error) {
	data, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, fmt.Errorf("googleworkspace: reading credentials file: %w", err)
	}
	var key serviceAccountKey
	if err := json.Unmarshal(data, &key); err != nil {
		return nil, fmt.Errorf("googleworkspace: parsing credentials file: %w", err)
	}
	if key.Type != "service_account" {
		return nil, fmt.Errorf("googleworkspace: credentials type %q is not service_account", key.Type)
	}
	block, _ := pem.Decode([]byte(key.PrivateKey))
	if block == nil {
		return nil, fmt.Errorf("googleworkspace: could not decode PEM private key from credentials file")
	}
	raw, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("googleworkspace: parsing private key: %w", err)
	}
	rsaKey, ok := raw.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("googleworkspace: private key is not RSA")
	}
	tokenURI := key.TokenURI
	if tokenURI == "" {
		tokenURI = tokenEndpoint
	}
	return &Client{
		adminEmail:  adminEmail,
		domain:      domain,
		ouPaths:     ouPaths,
		privateKey:  rsaKey,
		clientEmail: key.ClientEmail,
		tokenURI:    tokenURI,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// ListActiveUsers returns all active (non-suspended, non-archived) users. If
// ouPaths is configured, only users in those OUs (and subtrees) are returned;
// duplicates across overlapping OUs are deduplicated by user ID.
func (c *Client) ListActiveUsers(ctx context.Context) ([]User, error) {
	if err := c.ensureToken(ctx); err != nil {
		return nil, err
	}
	if len(c.ouPaths) > 0 {
		return c.listByOUs(ctx)
	}
	return c.listUsersForOU(ctx, "")
}

func (c *Client) listByOUs(ctx context.Context) ([]User, error) {
	seen := make(map[string]struct{})
	var all []User
	for _, ou := range c.ouPaths {
		users, err := c.listUsersForOU(ctx, ou)
		if err != nil {
			return nil, err
		}
		for _, u := range users {
			if _, dup := seen[u.ID]; !dup {
				seen[u.ID] = struct{}{}
				all = append(all, u)
			}
		}
	}
	return all, nil
}

func (c *Client) listUsersForOU(ctx context.Context, ouPath string) ([]User, error) {
	var all []User
	pageToken := ""
	for {
		params := url.Values{
			"domain":     {c.domain},
			"maxResults": {"500"},
			"query":      {"isSuspended=false"},
			"orderBy":    {"email"},
		}
		if ouPath != "" {
			params.Set("orgUnitPath", ouPath)
		}
		if pageToken != "" {
			params.Set("pageToken", pageToken)
		}
		var page usersListPage
		if err := c.get(ctx, apiBase+"/users?"+params.Encode(), &page); err != nil {
			return nil, err
		}
		for _, u := range page.Users {
			// isSuspended=false query excludes suspended users, but double-check
			// and also exclude archived users (Google Workspace for Education).
			if !u.Suspended && !u.Archived {
				all = append(all, u)
			}
		}
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return all, nil
}

type usersListPage struct {
	Users         []User `json:"users"`
	NextPageToken string `json:"nextPageToken"`
}

// --- HTTP helper ---

func (c *Client) get(ctx context.Context, endpoint string, out any) error {
	c.mu.Lock()
	token := c.accessToken
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("googleworkspace: GET %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("googleworkspace: GET %s: status %d: %s", endpoint, resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// --- Token management ---

// ensureToken obtains or refreshes the access token, using a 30-second buffer
// before the actual expiry so in-flight requests don't race the expiry.
func (c *Client) ensureToken(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessToken != "" && time.Now().Before(c.tokenExpiry.Add(-30*time.Second)) {
		return nil
	}
	token, expiry, err := c.fetchToken(ctx)
	if err != nil {
		return err
	}
	c.accessToken = token
	c.tokenExpiry = expiry
	return nil
}

func (c *Client) fetchToken(ctx context.Context) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(time.Hour)

	jwt, err := c.buildJWT(now, exp)
	if err != nil {
		return "", time.Time{}, err
	}

	form := url.Values{
		"grant_type": {jwtGrantType},
		"assertion":  {jwt},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURI,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("googleworkspace: token exchange: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", time.Time{}, fmt.Errorf("googleworkspace: token exchange: status %d: %s", resp.StatusCode, string(body))
	}
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", time.Time{}, fmt.Errorf("googleworkspace: token exchange: decode: %w", err)
	}
	return tr.AccessToken, now.Add(time.Duration(tr.ExpiresIn) * time.Second), nil
}

// buildJWT constructs the RS256-signed JWT assertion for the token exchange.
// The sub claim is the admin email being impersonated (not the service account).
func (c *Client) buildJWT(iat, exp time.Time) (string, error) {
	header := b64url(mustMarshal(map[string]string{"alg": "RS256", "typ": "JWT"}))
	claims := b64url(mustMarshal(map[string]any{
		"iss":   c.clientEmail,
		"sub":   c.adminEmail,
		"scope": directoryScope,
		"aud":   c.tokenURI,
		"iat":   iat.Unix(),
		"exp":   exp.Unix(),
	}))
	unsigned := header + "." + claims

	h := sha256.New()
	h.Write([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.privateKey, crypto.SHA256, h.Sum(nil))
	if err != nil {
		return "", fmt.Errorf("googleworkspace: signing JWT: %w", err)
	}
	return unsigned + "." + b64url(sig), nil
}

func b64url(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
