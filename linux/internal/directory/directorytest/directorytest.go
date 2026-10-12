// Package directorytest fakes an LDAP directory for tests: a Dialer whose
// connections answer binds and user searches from a list of users.
package directorytest

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/go-ldap/ldap/v3"

	"sentinel-linux/internal/directory"
)

// User is an account in the fake directory.
type User struct {
	Name     string // matched by the user filter's {name}
	DN       string
	Password string
	Groups   []string // memberOf values
	GUID     []byte   // objectGUID (16 bytes) or nil
	Disabled bool     // userAccountControl ACCOUNTDISABLE
}

// LDAP is the fake directory. Set Down to make every dial fail.
type LDAP struct {
	BindDN, BindPassword string
	Users                []User

	mu      sync.Mutex
	Down    bool
	Filters []string // every search filter, in order
	Binds   []string // every bind DN, in order
}

// Dial is a directory.Dialer.
func (f *LDAP) Dial(_ context.Context, _ directory.LDAPSettings) (directory.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Down {
		return nil, errors.New("connection refused")
	}
	return &conn{f: f}, nil
}

// LastFilter is the most recent search filter.
func (f *LDAP) LastFilter() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Filters) == 0 {
		return ""
	}
	return f.Filters[len(f.Filters)-1]
}

type conn struct {
	f     *LDAP
	bound bool
}

func (c *conn) Bind(dn, password string) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.Binds = append(c.f.Binds, dn)
	if password == "" {
		return errors.New("empty password")
	}
	if dn == c.f.BindDN && password == c.f.BindPassword {
		c.bound = true
		return nil
	}
	for _, u := range c.f.Users {
		if strings.EqualFold(u.DN, dn) && u.Password == password {
			return nil
		}
	}
	return ldap.NewError(ldap.LDAPResultInvalidCredentials, errors.New("invalid credentials"))
}

// Search matches users whose escaped name is the filter's value: the
// filter must equal the template with {name} replaced, which the test
// settings make "(uid={name})".
func (c *conn) Search(req *ldap.SearchRequest) (*ldap.SearchResult, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.Filters = append(c.f.Filters, req.Filter)
	if c.f.BindDN != "" && !c.bound {
		return nil, ldap.NewError(ldap.LDAPResultInsufficientAccessRights, errors.New("bind first"))
	}
	if _, err := ldap.CompileFilter(req.Filter); err != nil {
		return nil, ldap.NewError(ldap.LDAPResultFilterError, err)
	}
	res := &ldap.SearchResult{}
	for _, u := range c.f.Users {
		if !strings.EqualFold(req.Filter, "(uid="+ldap.EscapeFilter(u.Name)+")") { // directories match names case-insensitively
			continue
		}
		e := ldap.NewEntry(u.DN, map[string][]string{"memberOf": u.Groups})
		if u.GUID != nil {
			e.Attributes = append(e.Attributes, &ldap.EntryAttribute{Name: "objectGUID", Values: []string{string(u.GUID)}, ByteValues: [][]byte{u.GUID}})
		}
		if u.Disabled {
			e.Attributes = append(e.Attributes, ldap.NewEntryAttribute("userAccountControl", []string{"514"}))
		}
		res.Entries = append(res.Entries, e)
	}
	return res, nil
}

func (c *conn) Close() error { return nil }
