package controller

import (
	"context"

	"github.com/greyrock-labs/dexd-mikrotik/internal/dns"
	"github.com/greyrock-labs/dexd-mikrotik/internal/source"
)

// Event is a lightweight, source-agnostic notification that desired
// state may have changed. The controller uses it as a debounce trigger
// and to log which container caused the change.
type Event struct {
	Action string
	Name   string
}

// Source supplies desired endpoints and a stream of change notifications.
type Source interface {
	Endpoints(ctx context.Context) ([]*source.Endpoint, error)
	Events(ctx context.Context) (<-chan Event, <-chan error)
}

// Provider is the DNS backend the controller drives.
type Provider interface {
	ListRecords(ctx context.Context) ([]dns.Record, error)
	CreateRecord(ctx context.Context, r dns.Record) (dns.Record, error)
	UpdateRecord(ctx context.Context, r dns.Record) (dns.Record, error)
	DeleteRecord(ctx context.Context, id, name, recordType string) error
}

// RecordValidator is an optional provider capability hook. Providers can use it
// to reject records that their backend cannot represent without failing the
// whole reconcile.
type RecordValidator interface {
	ValidateRecord(r dns.Record) error
}
