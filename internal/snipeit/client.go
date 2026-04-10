package snipeit

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// Client is a Snipe-IT REST API client, rate-limited to 2 req/s.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	limiter    *rate.Limiter
}

func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		limiter: rate.NewLimiter(rate.Every(500*time.Millisecond), 1),
	}
}

// --- Types ---

type License struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	Seats          int    `json:"seats"`
	FreeSeatsCount int    `json:"free_seats_count"`
}

type AssignedTo struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

type LicenseSeat struct {
	ID         int         `json:"id"`
	LicenseID  int         `json:"license_id"`
	AssignedTo *AssignedTo `json:"assigned_to"`
	Notes      string      `json:"notes"`
}

type SnipeUser struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

type Manufacturer struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// --- envelope types for POST/PATCH responses ---

type envelope struct {
	Status  string          `json:"status"`
	Message json.RawMessage `json:"messages"`
	Payload json.RawMessage `json:"payload"`
}

type licenseListResponse struct {
	Total int       `json:"total"`
	Rows  []License `json:"rows"`
}

type seatListResponse struct {
	Total int           `json:"total"`
	Rows  []LicenseSeat `json:"rows"`
}

type userListResponse struct {
	Total int         `json:"total"`
	Rows  []SnipeUser `json:"rows"`
}

type manufacturerListResponse struct {
	Total int            `json:"total"`
	Rows  []Manufacturer `json:"rows"`
}

// --- License methods ---

// FindLicenseByName searches for a license by exact name. Returns nil, nil if not found.
func (c *Client) FindLicenseByName(ctx context.Context, name string) (*License, error) {
	endpoint := fmt.Sprintf("/api/v1/licenses?search=%s&limit=50", url.QueryEscape(name))
	var result licenseListResponse
	if err := c.get(ctx, endpoint, &result); err != nil {
		return nil, err
	}
	for i := range result.Rows {
		if strings.EqualFold(result.Rows[i].Name, name) {
			return &result.Rows[i], nil
		}
	}
	return nil, nil
}

// FindLicenseByID fetches a license by numeric ID.
func (c *Client) FindLicenseByID(ctx context.Context, id int) (*License, error) {
	var lic License
	if err := c.get(ctx, fmt.Sprintf("/api/v1/licenses/%d", id), &lic); err != nil {
		return nil, err
	}
	return &lic, nil
}

// CreateLicense creates a new license record. categoryID is required. manufacturerID
// and supplierID are optional — pass 0 to omit them from the request.
func (c *Client) CreateLicense(ctx context.Context, name string, seats, categoryID, manufacturerID, supplierID int) (*License, error) {
	body := map[string]any{"name": name, "seats": seats, "category_id": categoryID}
	if manufacturerID != 0 {
		body["manufacturer_id"] = manufacturerID
	}
	if supplierID != 0 {
		body["supplier_id"] = supplierID
	}
	var env envelope
	if err := c.post(ctx, "/api/v1/licenses", body, &env); err != nil {
		return nil, err
	}
	if env.Status != "success" {
		return nil, fmt.Errorf("snipeit CreateLicense: status=%q messages=%s", env.Status, string(env.Message))
	}
	var lic License
	if err := json.Unmarshal(env.Payload, &lic); err != nil {
		return nil, fmt.Errorf("snipeit CreateLicense: unmarshal payload: %w", err)
	}
	return &lic, nil
}

// FindOrCreateLicense finds the license by name, creating it if absent.
// categoryID is required; manufacturerID and supplierID are optional (pass 0 to omit).
func (c *Client) FindOrCreateLicense(ctx context.Context, name string, initialSeats, categoryID, manufacturerID, supplierID int) (*License, error) {
	lic, err := c.FindLicenseByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if lic != nil {
		return lic, nil
	}
	return c.CreateLicense(ctx, name, initialSeats, categoryID, manufacturerID, supplierID)
}

// --- Manufacturer methods ---

// FindManufacturerByName searches for a manufacturer by exact name. Returns nil, nil if not found.
func (c *Client) FindManufacturerByName(ctx context.Context, name string) (*Manufacturer, error) {
	endpoint := fmt.Sprintf("/api/v1/manufacturers?search=%s&limit=50", url.QueryEscape(name))
	var result manufacturerListResponse
	if err := c.get(ctx, endpoint, &result); err != nil {
		return nil, err
	}
	for i := range result.Rows {
		if strings.EqualFold(result.Rows[i].Name, name) {
			return &result.Rows[i], nil
		}
	}
	return nil, nil
}

// CreateManufacturer creates a new manufacturer record.
func (c *Client) CreateManufacturer(ctx context.Context, name, mfrURL string) (*Manufacturer, error) {
	body := map[string]any{"name": name, "url": mfrURL}
	var env envelope
	if err := c.post(ctx, "/api/v1/manufacturers", body, &env); err != nil {
		return nil, err
	}
	if env.Status != "success" {
		return nil, fmt.Errorf("snipeit CreateManufacturer: status=%q messages=%s", env.Status, string(env.Message))
	}
	var mfr Manufacturer
	if err := json.Unmarshal(env.Payload, &mfr); err != nil {
		return nil, fmt.Errorf("snipeit CreateManufacturer: unmarshal payload: %w", err)
	}
	return &mfr, nil
}

// FindOrCreateManufacturer finds a manufacturer by name, creating it if absent.
func (c *Client) FindOrCreateManufacturer(ctx context.Context, name, mfrURL string) (*Manufacturer, error) {
	mfr, err := c.FindManufacturerByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if mfr != nil {
		return mfr, nil
	}
	return c.CreateManufacturer(ctx, name, mfrURL)
}

// UpdateLicenseSeats changes the seat count on an existing license.
func (c *Client) UpdateLicenseSeats(ctx context.Context, licenseID, seats int) (*License, error) {
	body := map[string]any{"seats": seats}
	var env envelope
	if err := c.patch(ctx, fmt.Sprintf("/api/v1/licenses/%d", licenseID), body, &env); err != nil {
		return nil, err
	}
	var lic License
	if err := json.Unmarshal(env.Payload, &lic); err != nil {
		return nil, fmt.Errorf("snipeit UpdateLicenseSeats: unmarshal payload: %w", err)
	}
	return &lic, nil
}

// --- Seat methods ---

// ListLicenseSeats returns all seats for a license (up to 500).
func (c *Client) ListLicenseSeats(ctx context.Context, licenseID int) ([]LicenseSeat, error) {
	var result seatListResponse
	if err := c.get(ctx, fmt.Sprintf("/api/v1/licenses/%d/seats?limit=500", licenseID), &result); err != nil {
		return nil, err
	}
	return result.Rows, nil
}

// CheckoutSeat assigns a seat to a Snipe-IT user via PATCH (POST returns 405).
func (c *Client) CheckoutSeat(ctx context.Context, licenseID, seatID, userID int, notes string) error {
	body := map[string]any{
		"assigned_to": userID,
		"notes":       notes,
	}
	var env envelope
	if err := c.patch(ctx, fmt.Sprintf("/api/v1/licenses/%d/seats/%d", licenseID, seatID), body, &env); err != nil {
		return err
	}
	if env.Status != "success" {
		return fmt.Errorf("snipeit CheckoutSeat license=%d seat=%d: status=%q messages=%s", licenseID, seatID, env.Status, string(env.Message))
	}
	return nil
}

// CheckinSeat returns a seat via DELETE.
func (c *Client) CheckinSeat(ctx context.Context, licenseID, seatID int) error {
	if err := c.limiter.Wait(ctx); err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodDelete,
		fmt.Sprintf("%s/api/v1/licenses/%d/seats/%d", c.baseURL, licenseID, seatID), nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("snipeit CheckinSeat license=%d seat=%d: status %d", licenseID, seatID, resp.StatusCode)
	}
	return nil
}

// UpdateSeatNotes patches the notes field on an existing checkout.
func (c *Client) UpdateSeatNotes(ctx context.Context, licenseID, seatID int, notes string) error {
	body := map[string]any{"notes": notes}
	var env envelope
	return c.patch(ctx, fmt.Sprintf("/api/v1/licenses/%d/seats/%d", licenseID, seatID), body, &env)
}

// --- User methods ---

// FindUserByEmail searches Snipe-IT users by email. Returns nil, nil if not found.
func (c *Client) FindUserByEmail(ctx context.Context, email string) (*SnipeUser, error) {
	endpoint := fmt.Sprintf("/api/v1/users?search=%s&limit=50", url.QueryEscape(email))
	var result userListResponse
	if err := c.get(ctx, endpoint, &result); err != nil {
		return nil, err
	}
	needle := strings.ToLower(email)
	for i := range result.Rows {
		if strings.ToLower(result.Rows[i].Email) == needle {
			return &result.Rows[i], nil
		}
	}
	return nil, nil
}

// CreateUser creates a new Snipe-IT user. The user is created with login
// disabled (activated=false), no welcome email, and no auto-assign license
// group membership. startDate must be "YYYY-MM-DD" or empty to omit.
func (c *Client) CreateUser(ctx context.Context, firstName, lastName, email, username, notes, startDate string) (*SnipeUser, error) {
	pw, err := randomPassword()
	if err != nil {
		return nil, fmt.Errorf("snipeit CreateUser: generating password: %w", err)
	}
	body := map[string]any{
		"first_name":            firstName,
		"last_name":             lastName,
		"email":                 email,
		"username":              username,
		"password":              pw,
		"password_confirmation": pw,
		"activated":             false,
		"send_welcome":          false,
		"notes":                 notes,
	}
	if startDate != "" {
		body["start_date"] = startDate
	}
	var env envelope
	if err := c.post(ctx, "/api/v1/users", body, &env); err != nil {
		return nil, err
	}
	if env.Status != "success" {
		return nil, fmt.Errorf("snipeit CreateUser: status=%q messages=%s", env.Status, string(env.Message))
	}
	var u SnipeUser
	if err := json.Unmarshal(env.Payload, &u); err != nil {
		return nil, fmt.Errorf("snipeit CreateUser: unmarshal payload: %w", err)
	}
	return &u, nil
}

// randomPassword generates a cryptographically random 32-hex-character password.
func randomPassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// --- HTTP helpers ---

func (c *Client) get(ctx context.Context, path string, out any) error {
	if err := c.limiter.Wait(ctx); err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("snipeit GET %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	return c.writeRequest(ctx, http.MethodPost, path, body, out)
}

func (c *Client) patch(ctx context.Context, path string, body any, out any) error {
	return c.writeRequest(ctx, http.MethodPatch, path, body, out)
}

func (c *Client) writeRequest(ctx context.Context, method, path string, body any, out any) error {
	if err := c.limiter.Wait(ctx); err != nil {
		return err
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, method, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("snipeit %s %s: status %d", method, path, resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, url string, body *bytes.Reader) (*http.Request, error) {
	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequestWithContext(ctx, method, url, body)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, url, nil)
	}
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	return req, nil
}
