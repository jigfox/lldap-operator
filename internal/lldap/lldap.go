// Package lldap is a minimal client for the lldap GraphQL API.
package lldap

import (
	"context"
	"errors"
)

// ErrNotFound is returned when the requested user does not exist.
var ErrNotFound = errors.New("lldap: not found")

// Group is an lldap group.
type Group struct {
	ID          int64
	DisplayName string
}

// User is an lldap user with the groups it is a member of.
type User struct {
	ID          string
	Email       string
	DisplayName string
	FirstName   string
	LastName    string
	Groups      []Group
}

// CreateUserInput holds the fields sent to lldap when creating a user.
type CreateUserInput struct {
	ID          string
	Email       string
	DisplayName string
	FirstName   string
	LastName    string
}

// UpdateUserInput holds the fields to change on a user. Nil fields are not sent
// and keep their value in lldap.
type UpdateUserInput struct {
	ID          string
	Email       *string
	DisplayName *string
	FirstName   *string
	LastName    *string
}

// Client is what the controllers need from lldap.
type Client interface {
	// GetUser returns the user, or ErrNotFound.
	GetUser(ctx context.Context, id string) (*User, error)
	CreateUser(ctx context.Context, in CreateUserInput) error
	UpdateUser(ctx context.Context, in UpdateUserInput) error
	DeleteUser(ctx context.Context, id string) error

	ListGroups(ctx context.Context) ([]Group, error)
	CreateGroup(ctx context.Context, displayName string) (Group, error)
	UpdateGroup(ctx context.Context, id int64, displayName string) error
	DeleteGroup(ctx context.Context, id int64) error

	AddUserToGroup(ctx context.Context, userID string, groupID int64) error
	RemoveUserFromGroup(ctx context.Context, userID string, groupID int64) error
}
