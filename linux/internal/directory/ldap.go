package directory

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// LDAPTimeout bounds every LDAP operation (connect, bind, search).
const LDAPTimeout = 10 * time.Second

// Conn is the part of an LDAP connection sign-in needs; *ldap.Conn
// implements it, tests fake it.
type Conn interface {
	Bind(username, password string) error
	Search(req *ldap.SearchRequest) (*ldap.SearchResult, error)
	Close() error
}

// Dialer opens a connection (TLS already negotiated) to the directory.
type Dialer func(ctx context.Context, s LDAPSettings) (Conn, error)

// DialLDAP connects with ldaps:// or ldap:// + StartTLS, with LDAPTimeout
// on the connection and on every request.
func DialLDAP(ctx context.Context, s LDAPSettings) (Conn, error) {
	u, err := url.Parse(s.URL)
	if err != nil {
		return nil, err
	}
	tlsConf := &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12, InsecureSkipVerify: s.InsecureSkipVerify} //nolint:gosec // only when the administrator turned it on
	d := &net.Dialer{Timeout: LDAPTimeout}
	if deadline, ok := ctx.Deadline(); ok {
		d.Deadline = deadline
	}
	c, err := ldap.DialURL(s.URL, ldap.DialWithDialer(d), ldap.DialWithTLSConfig(tlsConf))
	if err != nil {
		return nil, err
	}
	c.SetTimeout(LDAPTimeout)
	if s.StartTLS {
		if err := c.StartTLS(tlsConf); err != nil {
			c.Close()
			return nil, fmt.Errorf("StartTLS: %w", err)
		}
	}
	return c, nil
}

// LDAP checks names and passwords against a directory.
type LDAP struct {
	Settings LDAPSettings // with WithDefaults applied
	Dial     Dialer       // nil = DialLDAP
}

// maxNameLength bounds what is put into a filter.
const maxNameLength = 256

// Authenticate finds the user with the service account, binds as them to
// check the password and maps their groups to a role. On ErrNoRole the
// identity (with its groups) is returned too, for the settings test.
func (l *LDAP) Authenticate(ctx context.Context, name, password string) (*Identity, error) {
	name = strings.TrimSpace(name)
	// An empty password would be an "unauthenticated bind", which many
	// directories accept: never let it through.
	if name == "" || password == "" || len(name) > maxNameLength {
		return nil, ErrBadCredentials
	}
	return l.run(ctx, func(c Conn) (*Identity, error) {
		entry, err := l.findUser(c, name)
		if err != nil {
			return nil, err
		}
		if err := c.Bind(entry.DN, password); err != nil {
			if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
				return nil, ErrBadCredentials
			}
			return nil, &UnavailableError{err}
		}
		return l.identity(entry, name)
	})
}

// Lookup finds a user by name with the service account alone (no
// password) and maps their groups again: a refresh of an API sign-in uses
// it to notice users who left the directory or lost their groups.
func (l *LDAP) Lookup(ctx context.Context, name string) (*Identity, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxNameLength {
		return nil, ErrBadCredentials
	}
	return l.run(ctx, func(c Conn) (*Identity, error) {
		entry, err := l.findUser(c, name)
		if err != nil {
			return nil, err
		}
		return l.identity(entry, name)
	})
}

func (l *LDAP) run(ctx context.Context, f func(Conn) (*Identity, error)) (*Identity, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*LDAPTimeout)
	defer cancel()
	dial := l.Dial
	if dial == nil {
		dial = DialLDAP
	}
	c, err := dial(ctx, l.Settings)
	if err != nil {
		return nil, &UnavailableError{err}
	}
	// Closing the connection unblocks whatever request is in flight.
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer func() {
		if stop() {
			c.Close()
		}
	}()
	id, err := f(c)
	if ctx.Err() != nil && err != nil {
		return nil, &UnavailableError{ctx.Err()}
	}
	return id, err
}

const uacAccountDisable = 0x2 // userAccountControl ACCOUNTDISABLE (Active Directory)

func (l *LDAP) findUser(c Conn, name string) (*ldap.Entry, error) {
	if l.Settings.BindDN != "" {
		if err := c.Bind(l.Settings.BindDN, l.Settings.BindPassword); err != nil {
			return nil, &UnavailableError{fmt.Errorf("service account bind: %w", err)}
		}
	}
	req := ldap.NewSearchRequest(l.Settings.UserBaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		2, int(LDAPTimeout/time.Second), false, userFilter(l.Settings.UserFilter, name),
		[]string{l.Settings.GroupAttribute, "objectGUID", "entryUUID", "userAccountControl"}, nil)
	res, err := c.Search(req)
	if ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded) {
		return nil, ErrBadCredentials // ambiguous name: refuse rather than guess
	}
	if ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject) {
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, &UnavailableError{fmt.Errorf("user search: %w", err)}
	}
	if len(res.Entries) != 1 {
		return nil, ErrBadCredentials
	}
	e := res.Entries[0]
	if v := e.GetAttributeValue("userAccountControl"); v != "" {
		var uac int64
		if _, err := fmt.Sscan(v, &uac); err == nil && uac&uacAccountDisable != 0 {
			return nil, ErrDisabled
		}
	}
	return e, nil
}

func (l *LDAP) identity(e *ldap.Entry, name string) (*Identity, error) {
	id := &Identity{ExternalID: externalID(e), Name: name, Groups: e.GetEqualFoldAttributeValues(l.Settings.GroupAttribute)}
	if id.Groups == nil {
		id.Groups = []string{}
	}
	m, ok := MatchRole(l.Settings.RoleMappings, id.Groups, SameDN)
	if !ok {
		return id, ErrNoRole
	}
	id.Mapping = m
	return id, nil
}

// externalID is objectGUID (Active Directory), entryUUID (OpenLDAP and
// others) or, failing both, the DN.
func externalID(e *ldap.Entry) string {
	if g := e.GetRawAttributeValue("objectGUID"); len(g) == 16 {
		return "guid:" + hex.EncodeToString(g)
	}
	if u := e.GetAttributeValue("entryUUID"); u != "" {
		return "uuid:" + strings.ToLower(u)
	}
	return "dn:" + e.DN
}

// userFilter puts the escaped name into the filter template, so a name
// like "*)(uid=*" matches only itself.
func userFilter(template, name string) string {
	return strings.ReplaceAll(template, "{name}", ldap.EscapeFilter(name))
}

// SameDN compares two DNs as the directory would (case- and
// spacing-insensitive), falling back to a case-insensitive string match
// for values that aren't DNs.
func SameDN(a, b string) bool {
	da, errA := ldap.ParseDN(a)
	db, errB := ldap.ParseDN(b)
	if errA == nil && errB == nil && len(da.RDNs) > 0 {
		return da.EqualFold(db)
	}
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// Describe is a short, secret-free summary for the settings test.
func Describe(err error) string {
	var ue *UnavailableError
	switch {
	case err == nil:
		return "signed in"
	case errors.Is(err, ErrBadCredentials):
		return "the directory didn't accept that name and password (or found no single user with that name)"
	case errors.As(err, &ue):
		return ue.Error()
	default:
		return err.Error()
	}
}
