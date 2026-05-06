package store

import "fmt"

// Domain identifies one of the five GitScale schema domains.
type Domain string

const (
	DomainIdentity      Domain = "identity"
	DomainRepositories  Domain = "repositories"
	DomainCollaboration Domain = "collaboration"
	DomainCI            Domain = "ci"
	DomainBilling       Domain = "billing"
)

// Valid reports whether d is a known domain constant.
func (d Domain) Valid() bool {
	switch d {
	case DomainIdentity, DomainRepositories, DomainCollaboration, DomainCI, DomainBilling:
		return true
	}
	return false
}

// OutboxTable returns the schema-qualified outbox table name for d.
// Panics if d is not a valid domain.
func (d Domain) OutboxTable() string {
	if !d.Valid() {
		panic(fmt.Sprintf("store: invalid domain %q", d))
	}
	return string(d) + "." + string(d) + "_outbox"
}
