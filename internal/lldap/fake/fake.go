// Package fake is an in-memory lldap.Client for tests.
package fake

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/jigfox/lldap-operator/internal/lldap"
)

// Client is an in-memory lldap.Client. Every mutation is appended to Calls.
type Client struct {
	mu     sync.Mutex
	users  map[string]*lldap.User
	groups map[int64]string
	nextID int64
	calls  []string

	// Err, if set, is returned by every method.
	Err error
}

var _ lldap.Client = (*Client)(nil)

// New returns an empty fake with the built-in lldap_* groups and the admin user.
func New() *Client {
	c := &Client{users: map[string]*lldap.User{}, groups: map[int64]string{}, nextID: 1}
	for _, g := range []string{"lldap_admin", "lldap_password_manager", "lldap_strict_readonly"} {
		c.addGroup(g)
	}
	c.users["admin"] = &lldap.User{ID: "admin", Email: "admin@example.com"}
	_ = c.addMember("admin", 1)
	return c
}

func (c *Client) addGroup(name string) int64 {
	id := c.nextID
	c.nextID++
	c.groups[id] = name
	return id
}

func (c *Client) addMember(user string, group int64) error {
	u, ok := c.users[user]
	if !ok {
		return fmt.Errorf("%w: user %s", lldap.ErrNotFound, user)
	}
	name, ok := c.groups[group]
	if !ok {
		return fmt.Errorf("%w: group %d", lldap.ErrNotFound, group)
	}
	for _, g := range u.Groups {
		if g.ID == group {
			return nil
		}
	}
	u.Groups = append(u.Groups, lldap.Group{ID: group, DisplayName: name})
	return nil
}

// Calls returns the mutations seen so far, e.g. "CreateUser alice".
func (c *Client) Calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

// ResetCalls forgets the recorded mutations.
func (c *Client) ResetCalls() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = nil
}

// Seed adds a user without recording a call. Group names are created if missing.
func (c *Client) Seed(u lldap.User, groups ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := u
	cp.Groups = nil
	c.users[u.ID] = &cp
	for _, name := range groups {
		id := c.groupID(name)
		if id == 0 {
			id = c.addGroup(name)
		}
		_ = c.addMember(u.ID, id)
	}
}

// SeedGroup adds a group without recording a call and returns its id.
func (c *Client) SeedGroup(name string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id := c.groupID(name); id != 0 {
		return id
	}
	return c.addGroup(name)
}

func (c *Client) groupID(name string) int64 {
	for id, n := range c.groups {
		if n == name {
			return id
		}
	}
	return 0
}

// User returns a copy of the stored user, or nil.
func (c *Client) User(id string) *lldap.User {
	c.mu.Lock()
	defer c.mu.Unlock()
	u, ok := c.users[id]
	if !ok {
		return nil
	}
	cp := *u
	cp.Groups = slices.Clone(u.Groups)
	return &cp
}

// GroupNames returns the display names of all groups.
func (c *Client) GroupNames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, 0, len(c.groups))
	for _, n := range c.groups {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

func (c *Client) record(format string, args ...any) {
	c.calls = append(c.calls, fmt.Sprintf(format, args...))
}

// GetUser implements lldap.Client.
func (c *Client) GetUser(_ context.Context, id string) (*lldap.User, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return nil, c.Err
	}
	u, ok := c.users[id]
	if !ok {
		return nil, fmt.Errorf("%w: user %s", lldap.ErrNotFound, id)
	}
	cp := *u
	cp.Groups = slices.Clone(u.Groups)
	return &cp, nil
}

// CreateUser implements lldap.Client.
func (c *Client) CreateUser(_ context.Context, in lldap.CreateUserInput) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return c.Err
	}
	if _, ok := c.users[in.ID]; ok {
		return fmt.Errorf("user %s already exists", in.ID)
	}
	c.record("CreateUser %s", in.ID)
	c.users[in.ID] = &lldap.User{
		ID: in.ID, Email: in.Email, DisplayName: in.DisplayName, FirstName: in.FirstName, LastName: in.LastName,
	}
	return nil
}

// UpdateUser implements lldap.Client.
func (c *Client) UpdateUser(_ context.Context, in lldap.UpdateUserInput) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return c.Err
	}
	u, ok := c.users[in.ID]
	if !ok {
		return fmt.Errorf("%w: user %s", lldap.ErrNotFound, in.ID)
	}
	c.record("UpdateUser %s", in.ID)
	if in.Email != nil {
		u.Email = *in.Email
	}
	if in.DisplayName != nil {
		u.DisplayName = *in.DisplayName
	}
	if in.FirstName != nil {
		u.FirstName = *in.FirstName
	}
	if in.LastName != nil {
		u.LastName = *in.LastName
	}
	return nil
}

// DeleteUser implements lldap.Client.
func (c *Client) DeleteUser(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return c.Err
	}
	if _, ok := c.users[id]; !ok {
		return fmt.Errorf("%w: user %s", lldap.ErrNotFound, id)
	}
	c.record("DeleteUser %s", id)
	delete(c.users, id)
	return nil
}

// ListGroups implements lldap.Client.
func (c *Client) ListGroups(_ context.Context) ([]lldap.Group, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return nil, c.Err
	}
	groups := make([]lldap.Group, 0, len(c.groups))
	for id, n := range c.groups {
		groups = append(groups, lldap.Group{ID: id, DisplayName: n})
	}
	slices.SortFunc(groups, func(a, b lldap.Group) int { return int(a.ID - b.ID) })
	return groups, nil
}

// CreateGroup implements lldap.Client.
func (c *Client) CreateGroup(_ context.Context, name string) (lldap.Group, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return lldap.Group{}, c.Err
	}
	if c.groupID(name) != 0 {
		return lldap.Group{}, fmt.Errorf("group %s already exists", name)
	}
	c.record("CreateGroup %s", name)
	return lldap.Group{ID: c.addGroup(name), DisplayName: name}, nil
}

// UpdateGroup implements lldap.Client.
func (c *Client) UpdateGroup(_ context.Context, id int64, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return c.Err
	}
	if _, ok := c.groups[id]; !ok {
		return fmt.Errorf("%w: group %d", lldap.ErrNotFound, id)
	}
	c.record("UpdateGroup %d %s", id, name)
	c.groups[id] = name
	for _, u := range c.users {
		for i := range u.Groups {
			if u.Groups[i].ID == id {
				u.Groups[i].DisplayName = name
			}
		}
	}
	return nil
}

// DeleteGroup implements lldap.Client.
func (c *Client) DeleteGroup(_ context.Context, id int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return c.Err
	}
	if _, ok := c.groups[id]; !ok {
		return fmt.Errorf("%w: group %d", lldap.ErrNotFound, id)
	}
	c.record("DeleteGroup %d", id)
	delete(c.groups, id)
	for _, u := range c.users {
		u.Groups = slices.DeleteFunc(u.Groups, func(g lldap.Group) bool { return g.ID == id })
	}
	return nil
}

// AddUserToGroup implements lldap.Client.
func (c *Client) AddUserToGroup(_ context.Context, userID string, groupID int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return c.Err
	}
	c.record("AddUserToGroup %s %s", userID, c.groups[groupID])
	return c.addMember(userID, groupID)
}

// RemoveUserFromGroup implements lldap.Client.
func (c *Client) RemoveUserFromGroup(_ context.Context, userID string, groupID int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return c.Err
	}
	u, ok := c.users[userID]
	if !ok {
		return fmt.Errorf("%w: user %s", lldap.ErrNotFound, userID)
	}
	c.record("RemoveUserFromGroup %s %s", userID, c.groups[groupID])
	u.Groups = slices.DeleteFunc(u.Groups, func(g lldap.Group) bool { return g.ID == groupID })
	return nil
}
