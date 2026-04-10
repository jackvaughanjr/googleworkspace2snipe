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
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	tokenEndpoint  = "https://oauth2.googleapis.com/token"
	licensingScope = "https://www.googleapis.com/auth/apps.licensing"
	directoryScope = "https://www.googleapis.com/auth/admin.directory.user.readonly"
	licensingBase  = "https://licensing.googleapis.com/apps/licensing/v1"
	directoryBase  = "https://admin.googleapis.com/admin/directory/v1"
	jwtGrantType   = "urn:ietf:params:oauth:grant-type:jwt-bearer"
)

// DefaultProductIDs is the list of Google Workspace product IDs queried when
// google_workspace.product_ids is not set in settings.yaml.
var DefaultProductIDs = []string{
	"Google-Apps",          // Google Workspace Business / Enterprise / Education
	"Google-Vault",         // Google Vault
	"Google-Drive-storage", // Google additional storage
}

// KnownProductIDs is the comprehensive list probed by the discover command.
// Only includes product IDs that are valid in the Enterprise License Manager API.
// Note: "Google Workspace add-on" subscription types (e.g. Google Voice Standard,
// AI Ultra Access) return 400 "Invalid productId" from the Licensing API and
// cannot be discovered or synced — they are not exposed through this API.
var KnownProductIDs = []string{
	"Google-Apps",          // Google Workspace Business / Enterprise / Education
	"Google-Vault",         // Google Vault
	"Google-Drive-storage", // Google additional storage
	"101031",               // Google Workspace Migrate
}

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

// SkuGroup represents one Google Workspace SKU and the set of users currently
// assigned to it. Each SkuGroup maps to one Snipe-IT license.
type SkuGroup struct {
	ProductID   string
	ProductName string
	SkuID       string
	SkuName     string
	UserEmails  []string // lower-cased, sorted
}

// User represents a Google Workspace directory user. Only populated when the
// Directory API scope is requested (OU filtering or note enrichment).
type User struct {
	ID           string   `json:"id"`
	PrimaryEmail string   `json:"primaryEmail"`
	Name         UserName `json:"name"`
	OrgUnitPath  string   `json:"orgUnitPath"`
	IsAdmin      bool     `json:"isAdmin"`
	Suspended    bool     `json:"suspended"`
	Archived     bool     `json:"archived"`
}

// UserName holds the name components from the Directory API.
type UserName struct {
	FullName   string `json:"fullName"`
	GivenName  string `json:"givenName"`
	FamilyName string `json:"familyName"`
}

// googleAPIError is the error envelope returned by Google APIs on failure.
type googleAPIError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

type licenseAssignment struct {
	UserID      string `json:"userId"`
	ProductID   string `json:"productId"`
	ProductName string `json:"productName"`
	SkuID       string `json:"skuId"`
	SkuName     string `json:"skuName"`
}

type licenseAssignmentPage struct {
	Items         []licenseAssignment `json:"items"`
	NextPageToken string              `json:"nextPageToken"`
}

type usersListPage struct {
	Users         []User `json:"users"`
	NextPageToken string `json:"nextPageToken"`
}

// Client calls the Google Enterprise License Manager API (and optionally the
// Directory API) using a service account with domain-wide delegation. Auth is
// performed via a self-signed JWT exchanged for a short-lived access token.
type Client struct {
	adminEmail  string
	domain      string
	privateKey  *rsa.PrivateKey
	clientEmail string
	tokenURI    string
	scope       string // space-separated OAuth2 scopes
	httpClient  *http.Client

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

// NewClientFromFile creates a Client from a service account JSON key file.
// Set withDirectory to true when OU filtering or note enrichment is configured —
// this adds the admin.directory.user.readonly scope to the JWT and requires that
// scope to be granted in the DWD entry alongside apps.licensing.
func NewClientFromFile(credentialsFile, adminEmail, domain string, withDirectory bool) (*Client, error) {
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
		return nil, fmt.Errorf("googleworkspace: could not decode PEM private key")
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
	scope := licensingScope
	if withDirectory {
		scope += " " + directoryScope
	}
	return &Client{
		adminEmail:  adminEmail,
		domain:      domain,
		privateKey:  rsaKey,
		clientEmail: key.ClientEmail,
		tokenURI:    tokenURI,
		scope:       scope,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// --- License Manager API ---

// ListLicenseAssignmentsBySku fetches all active license assignments for the
// given productIDs, grouped by SKU, sorted by SkuName.
func (c *Client) ListLicenseAssignmentsBySku(ctx context.Context, productIDs []string) ([]SkuGroup, error) {
	if err := c.ensureToken(ctx); err != nil {
		return nil, err
	}
	skuMap := make(map[string]*SkuGroup)
	for _, productID := range productIDs {
		assignments, err := c.listAssignmentsForProduct(ctx, productID)
		if err != nil {
			return nil, fmt.Errorf("googleworkspace: listing assignments for product %q: %w", productID, err)
		}
		for _, a := range assignments {
			key := a.ProductID + "/" + a.SkuID
			g, ok := skuMap[key]
			if !ok {
				g = &SkuGroup{
					ProductID:   a.ProductID,
					ProductName: a.ProductName,
					SkuID:       a.SkuID,
					SkuName:     a.SkuName,
				}
				skuMap[key] = g
			}
			g.UserEmails = append(g.UserEmails, strings.ToLower(a.UserID))
		}
	}
	groups := make([]SkuGroup, 0, len(skuMap))
	for _, g := range skuMap {
		sort.Strings(g.UserEmails)
		groups = append(groups, *g)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].SkuName < groups[j].SkuName
	})
	return groups, nil
}

func (c *Client) listAssignmentsForProduct(ctx context.Context, productID string) ([]licenseAssignment, error) {
	var all []licenseAssignment
	pageToken := ""
	for {
		params := url.Values{
			"customerId": {c.domain},
			"maxResults": {"1000"},
		}
		if pageToken != "" {
			params.Set("pageToken", pageToken)
		}
		endpoint := fmt.Sprintf("%s/product/%s/users?%s",
			licensingBase, url.PathEscape(productID), params.Encode())
		var page licenseAssignmentPage
		if err := c.get(ctx, endpoint, &page); err != nil {
			// 404 = product not provisioned; 400 "Invalid productId" = not a Licensing
			// API product (e.g. add-ons like Voice Standard purchased via Workspace).
			// Both are treated as "no assignments" rather than a fatal error.
			if strings.Contains(err.Error(), "status 404") ||
				(strings.Contains(err.Error(), "status 400") && strings.Contains(err.Error(), "Invalid productId")) {
				break
			}
			return nil, err
		}
		all = append(all, page.Items...)
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return all, nil
}

// ProbeProductHasAssignments returns true if the given productID has at least one
// active license assignment in the domain. A 404 response means the product is not
// provisioned (returns false, nil). Used by the discover command.
func (c *Client) ProbeProductHasAssignments(ctx context.Context, productID string) (bool, error) {
	if err := c.ensureToken(ctx); err != nil {
		return false, err
	}
	params := url.Values{
		"customerId": {c.domain},
		"maxResults": {"1"},
	}
	endpoint := fmt.Sprintf("%s/product/%s/users?%s",
		licensingBase, url.PathEscape(productID), params.Encode())
	var page licenseAssignmentPage
	if err := c.get(ctx, endpoint, &page); err != nil {
		// 404 = product not provisioned; 400 "Invalid productId" = unknown product.
		// Both mean "no assignments here" for discovery purposes.
		if strings.Contains(err.Error(), "status 404") ||
			(strings.Contains(err.Error(), "status 400") && strings.Contains(err.Error(), "Invalid productId")) {
			return false, nil
		}
		return false, err
	}
	return len(page.Items) > 0, nil
}

// --- Directory API ---

// GetUserMap returns a map of lowercased email → User for active users in the domain.
// If ouPaths is non-empty, only users in those OUs (and subtrees) are returned.
// Requires the Directory API scope (withDirectory=true at construction).
func (c *Client) GetUserMap(ctx context.Context, ouPaths []string) (map[string]User, error) {
	if err := c.ensureToken(ctx); err != nil {
		return nil, err
	}
	var allUsers []User
	if len(ouPaths) > 0 {
		seen := make(map[string]struct{})
		for _, ou := range ouPaths {
			users, err := c.getUsersForOU(ctx, ou)
			if err != nil {
				return nil, err
			}
			for _, u := range users {
				if _, dup := seen[u.ID]; !dup {
					seen[u.ID] = struct{}{}
					allUsers = append(allUsers, u)
				}
			}
		}
	} else {
		users, err := c.getUsersForOU(ctx, "")
		if err != nil {
			return nil, err
		}
		allUsers = users
	}
	m := make(map[string]User, len(allUsers))
	for _, u := range allUsers {
		m[strings.ToLower(u.PrimaryEmail)] = u
	}
	return m, nil
}

func (c *Client) getUsersForOU(ctx context.Context, ouPath string) ([]User, error) {
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
		if err := c.get(ctx, directoryBase+"/users?"+params.Encode(), &page); err != nil {
			return nil, err
		}
		for _, u := range page.Users {
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

// --- API validation ---

// ValidateAPIs probes each required Google API with a minimal request to verify
// access before running a full sync. It distinguishes between an API that is not
// enabled in the GCP project and a DWD scope that is missing in the Admin Console,
// so the error message tells the user exactly what to fix.
func (c *Client) ValidateAPIs(ctx context.Context) error {
	if err := c.ensureToken(ctx); err != nil {
		return err
	}

	// Always validate the Enterprise License Manager API.
	licensingProbe := fmt.Sprintf("%s/product/Google-Apps/users?customerId=%s&maxResults=1",
		licensingBase, url.QueryEscape(c.domain))
	if err := c.checkAPIAccess(ctx, licensingProbe,
		"Enterprise License Manager API (licensing.googleapis.com)",
		"Enable it in the GCP console: APIs & Services → Library → \"Enterprise License Manager API\"",
	); err != nil {
		return err
	}

	// Validate the Directory API only when the scope was requested.
	if strings.Contains(c.scope, directoryScope) {
		directoryProbe := fmt.Sprintf("%s/users?domain=%s&maxResults=1",
			directoryBase, url.QueryEscape(c.domain))
		if err := c.checkAPIAccess(ctx, directoryProbe,
			"Admin SDK API (admin.googleapis.com)",
			"Enable it in the GCP console: APIs & Services → Library → \"Admin SDK API\"",
		); err != nil {
			return err
		}
	}

	return nil
}

// checkAPIAccess makes a single probe request and returns a human-readable error
// if the API is disabled or the DWD scope is not granted. A 404 response is treated
// as success (product not provisioned but API is reachable).
func (c *Client) checkAPIAccess(ctx context.Context, endpoint, apiName, enableHint string) error {
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
		return fmt.Errorf("googleworkspace: checking %s: %w", apiName, err)
	}
	defer resp.Body.Close()

	// 200 and 404 both mean the API is reachable.
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var apiErr googleAPIError
	_ = json.Unmarshal(body, &apiErr)
	msg := apiErr.Error.Message

	if resp.StatusCode == http.StatusForbidden {
		if strings.Contains(msg, "has not been used") || strings.Contains(msg, "disabled") {
			return fmt.Errorf("googleworkspace: %s is not enabled in your GCP project\n  Hint: %s", apiName, enableHint)
		}
		// PERMISSION_DENIED — API is enabled but DWD scope is missing.
		return fmt.Errorf("googleworkspace: PERMISSION_DENIED accessing %s\n  Check that the required OAuth scope is granted in the Google Admin Console under Security → API controls → Domain-wide Delegation\n  Error: %s", apiName, msg)
	}

	return fmt.Errorf("googleworkspace: unexpected status %d from %s: %s", resp.StatusCode, apiName, string(body))
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
	jwt, err := c.buildJWT(now, now.Add(time.Hour))
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

func (c *Client) buildJWT(iat, exp time.Time) (string, error) {
	header := b64url(mustMarshal(map[string]string{"alg": "RS256", "typ": "JWT"}))
	claims := b64url(mustMarshal(map[string]any{
		"iss":   c.clientEmail,
		"sub":   c.adminEmail,
		"scope": c.scope,
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
