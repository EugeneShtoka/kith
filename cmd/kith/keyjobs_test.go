package main

import (
	"context"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// accountsOf answers Accounts with a fixed list.
type accountsOf []domain.Account

func (a accountsOf) Accounts(context.Context) ([]domain.Account, error) { return a, nil }

// The key jobs act on the Matrix account the rule picks; where it would ask, they
// refuse, naming the accounts, since no one is there to answer.
func TestTheKeyJobsTakeThePickedMatrixAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	me := domain.Account{Network: domain.ProtocolMatrix, On: domain.AccountRooms(domain.ProtocolMatrix, "0123456789abcdef"), Name: "@me:x"}
	work := domain.Account{Network: domain.ProtocolMatrix, On: domain.AccountRooms(domain.ProtocolMatrix, "fedcba9876543210"), Name: "@me:work.x"}
	tg := domain.Account{Network: domain.ProtocolTelegram, On: domain.AccountRooms(domain.ProtocolTelegram, "42"), Name: "tg"}

	if on, err := matrixAccount(ctx, accountsOf{tg, me}); err != nil || on != me.On {
		t.Errorf("one Matrix account = (%q, %v), want it", on, err)
	}
	work.Default = true
	if on, err := matrixAccount(ctx, accountsOf{me, work}); err != nil || on != work.On {
		t.Errorf("two with a default = (%q, %v), want the default", on, err)
	}
	work.Default = false
	if _, err := matrixAccount(ctx, accountsOf{me, work}); err == nil || !strings.Contains(err.Error(), "@me:work.x") {
		t.Errorf("two with no default = %v, want a refusal naming them", err)
	}
	if _, err := matrixAccount(ctx, accountsOf{tg}); err == nil {
		t.Error("no Matrix account was no error")
	}
}
