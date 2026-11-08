package lldap

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// HTTPClient talks to lldap over its HTTP API: a JWT from /auth/simple/login
// and GraphQL on /api/graphql. When lldap answers 401 (expired token) or the
// token is about to expire, it logs in again with the configured credentials.
type HTTPClient struct {
	baseURL  string
	username string
	password string
	http     *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

var _ Client = (*HTTPClient)(nil)

// tokenSkew is how long before its expiry a token is replaced.
const tokenSkew = 30 * time.Second

// NewHTTPClient returns a client for the lldap instance at baseURL, for
// example http://lldap:17170. A nil httpClient uses a default with a timeout.
func NewHTTPClient(baseURL, username, password string, httpClient *http.Client) *HTTPClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTPClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		http:     httpClient,
	}
}

type loginResponse struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refreshToken"`
}

func (c *HTTPClient) login(ctx context.Context) error {
	body, err := json.Marshal(map[string]string{"username": c.username, "password": c.password})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/auth/simple/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("lldap login: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("lldap login: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("lldap login: unexpected status %d", resp.StatusCode)
	}
	var lr loginResponse
	if err := json.Unmarshal(data, &lr); err != nil || lr.Token == "" {
		return errors.New("lldap login: response has no token")
	}
	c.token = lr.Token
	c.expires = jwtExpiry(lr.Token)
	return nil
}

// jwtExpiry reads the exp claim without verifying the signature (the server
// does that); a zero time means unknown.
func jwtExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

// bearer returns a valid token, logging in if there is none, it is about to
// expire, or force is set.
func (c *HTTPClient) bearer(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	expiring := !c.expires.IsZero() && time.Now().Add(tokenSkew).After(c.expires)
	if force || c.token == "" || expiring {
		if err := c.login(ctx); err != nil {
			return "", err
		}
	}
	return c.token, nil
}

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type gqlError struct {
	Message string `json:"message"`
}

type gqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []gqlError      `json:"errors"`
}

func (c *HTTPClient) doGraphQL(ctx context.Context, token string, payload []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/graphql", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	return c.http.Do(req)
}

// graphql runs a query or mutation and decodes its data into out.
func (c *HTTPClient) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	payload, err := json.Marshal(gqlRequest{Query: query, Variables: vars})
	if err != nil {
		return err
	}
	token, err := c.bearer(ctx, false)
	if err != nil {
		return err
	}
	resp, err := c.doGraphQL(ctx, token, payload)
	if err != nil {
		return fmt.Errorf("lldap request: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		if token, err = c.bearer(ctx, true); err != nil {
			return err
		}
		if resp, err = c.doGraphQL(ctx, token, payload); err != nil {
			return fmt.Errorf("lldap request: %w", err)
		}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("lldap response: %w", err)
	}
	var gr gqlResponse
	if err := json.Unmarshal(data, &gr); err != nil {
		return fmt.Errorf("lldap: unexpected response (status %d)", resp.StatusCode)
	}
	if len(gr.Errors) > 0 {
		msgs := make([]string, len(gr.Errors))
		for i, e := range gr.Errors {
			msgs[i] = e.Message
		}
		msg := strings.Join(msgs, "; ")
		if strings.Contains(strings.ToLower(msg), "not found") {
			return fmt.Errorf("%w: %s", ErrNotFound, msg)
		}
		return fmt.Errorf("lldap: %s", msg)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("lldap: unexpected status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(gr.Data, out)
}

const (
	queryUser = `query($id: String!) {
  user(userId: $id) { id email displayName firstName lastName groups { id displayName } }
}`
	queryGroups = `query { groups { id displayName } }`

	mutationCreateUser = `mutation($user: CreateUserInput!) { createUser(user: $user) { id } }`
	mutationUpdateUser = `mutation($user: UpdateUserInput!) { updateUser(user: $user) { ok } }`
	mutationDeleteUser = `mutation($id: String!) { deleteUser(userId: $id) { ok } }`

	mutationCreateGroup = `mutation($name: String!) { createGroup(name: $name) { id displayName } }`
	mutationUpdateGroup = `mutation($group: UpdateGroupInput!) { updateGroup(group: $group) { ok } }`
	mutationDeleteGroup = `mutation($id: Int!) { deleteGroup(groupId: $id) { ok } }`

	mutationAddMember    = `mutation($user: String!, $group: Int!) { addUserToGroup(userId: $user, groupId: $group) { ok } }`
	mutationRemoveMember = `mutation($user: String!, $group: Int!) { removeUserFromGroup(userId: $user, groupId: $group) { ok } }`
)

type gqlGroup struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"displayName"`
}

// GetUser implements Client.
func (c *HTTPClient) GetUser(ctx context.Context, id string) (*User, error) {
	var out struct {
		User struct {
			ID          string     `json:"id"`
			Email       string     `json:"email"`
			DisplayName string     `json:"displayName"`
			FirstName   string     `json:"firstName"`
			LastName    string     `json:"lastName"`
			Groups      []gqlGroup `json:"groups"`
		} `json:"user"`
	}
	if err := c.graphql(ctx, queryUser, map[string]any{"id": id}, &out); err != nil {
		return nil, err
	}
	u := &User{
		ID:          out.User.ID,
		Email:       out.User.Email,
		DisplayName: out.User.DisplayName,
		FirstName:   out.User.FirstName,
		LastName:    out.User.LastName,
	}
	for _, g := range out.User.Groups {
		u.Groups = append(u.Groups, Group(g))
	}
	return u, nil
}

// CreateUser implements Client.
func (c *HTTPClient) CreateUser(ctx context.Context, in CreateUserInput) error {
	user := map[string]any{"id": in.ID, "email": in.Email}
	if in.DisplayName != "" {
		user["displayName"] = in.DisplayName
	}
	if in.FirstName != "" {
		user["firstName"] = in.FirstName
	}
	if in.LastName != "" {
		user["lastName"] = in.LastName
	}
	return c.graphql(ctx, mutationCreateUser, map[string]any{"user": user}, nil)
}

// UpdateUser implements Client.
func (c *HTTPClient) UpdateUser(ctx context.Context, in UpdateUserInput) error {
	user := map[string]any{"id": in.ID}
	if in.Email != nil {
		user["email"] = *in.Email
	}
	if in.DisplayName != nil {
		user["displayName"] = *in.DisplayName
	}
	if in.FirstName != nil {
		user["firstName"] = *in.FirstName
	}
	if in.LastName != nil {
		user["lastName"] = *in.LastName
	}
	return c.graphql(ctx, mutationUpdateUser, map[string]any{"user": user}, nil)
}

// DeleteUser implements Client.
func (c *HTTPClient) DeleteUser(ctx context.Context, id string) error {
	return c.graphql(ctx, mutationDeleteUser, map[string]any{"id": id}, nil)
}

// ListGroups implements Client.
func (c *HTTPClient) ListGroups(ctx context.Context) ([]Group, error) {
	var out struct {
		Groups []gqlGroup `json:"groups"`
	}
	if err := c.graphql(ctx, queryGroups, nil, &out); err != nil {
		return nil, err
	}
	groups := make([]Group, len(out.Groups))
	for i, g := range out.Groups {
		groups[i] = Group(g)
	}
	return groups, nil
}

// CreateGroup implements Client.
func (c *HTTPClient) CreateGroup(ctx context.Context, displayName string) (Group, error) {
	var out struct {
		CreateGroup gqlGroup `json:"createGroup"`
	}
	if err := c.graphql(ctx, mutationCreateGroup, map[string]any{"name": displayName}, &out); err != nil {
		return Group{}, err
	}
	return Group(out.CreateGroup), nil
}

// UpdateGroup implements Client.
func (c *HTTPClient) UpdateGroup(ctx context.Context, id int64, displayName string) error {
	group := map[string]any{"id": id, "displayName": displayName}
	return c.graphql(ctx, mutationUpdateGroup, map[string]any{"group": group}, nil)
}

// DeleteGroup implements Client.
func (c *HTTPClient) DeleteGroup(ctx context.Context, id int64) error {
	return c.graphql(ctx, mutationDeleteGroup, map[string]any{"id": id}, nil)
}

// AddUserToGroup implements Client.
func (c *HTTPClient) AddUserToGroup(ctx context.Context, userID string, groupID int64) error {
	return c.graphql(ctx, mutationAddMember, map[string]any{"user": userID, "group": groupID}, nil)
}

// RemoveUserFromGroup implements Client.
func (c *HTTPClient) RemoveUserFromGroup(ctx context.Context, userID string, groupID int64) error {
	return c.graphql(ctx, mutationRemoveMember, map[string]any{"user": userID, "group": groupID}, nil)
}
