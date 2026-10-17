package signal

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// ErrNoAccount is a login for a name no [[signal.account]] has.
var ErrNoAccount = errors.New("signal: no such [[signal.account]]")

// deviceName is what the phone lists kith under (Signal → Settings → Linked devices).
const deviceName = "kith"

// scanNote says how to scan the code; the link follows, for drawing it elsewhere.
const scanNote = "On the phone, open Signal → Settings → Linked devices → Link new device, and scan this code. " +
	"If it will not scan here, draw it as a QR code elsewhere from this link: "

// LoginNetwork is Signal and its accounts as a login lists them.
func (a *Adapter) LoginNetwork() api.LoginNetwork {
	n := api.LoginNetwork{Network: "signal", Label: "Signal", Detail: "your phone, linked as a secondary device"}
	for _, account := range a.accountsNow() {
		n.Accounts = append(n.Accounts, api.LoginAccount{Name: account.Name})
	}
	return n
}

// Login links account to the Signal account of the phone that scans the code, setting
// the account up first when it is new ("").
func (a *Adapter) Login(ctx context.Context, account string, talk api.LoginTalk) (api.LoginEnd, error) {
	if account == "" {
		name, err := a.setUp(ctx, talk)
		if err != nil {
			return api.LoginEnd{}, err
		}
		account = name
	}
	if !a.configured(account) {
		return api.LoginEnd{}, fmt.Errorf("%w named %q", ErrNoAccount, account)
	}
	h, err := a.helperUp(ctx)
	if err != nil {
		return api.LoginEnd{}, err
	}
	stream, err := h.client.Link(ctx, connect.NewRequest(&signalv1.LinkRequest{DeviceName: deviceName}))
	if err != nil {
		return api.LoginEnd{}, fmt.Errorf("signal: link: %w", err)
	}
	defer func() { _ = stream.Close() }() // ends the linking, if it still runs
	var linked *signalv1.Device
	for linked == nil && stream.Receive() {
		switch step := stream.Msg().GetStep().(type) {
		case *signalv1.LinkResponse_Url:
			if err := talk.ShowQR(ctx, step.Url, scanNote+step.Url); err != nil {
				return api.LoginEnd{}, err
			}
		case *signalv1.LinkResponse_Linked:
			linked = step.Linked
		}
	}
	if linked == nil {
		if err := stream.Err(); err != nil {
			return api.LoginEnd{}, fmt.Errorf("signal: link: %w", err)
		}
		return api.LoginEnd{}, errors.New("signal: linking ended without an account")
	}
	return a.finish(account, h, linked)
}

// finish keeps which Signal account account is now linked to.
func (a *Adapter) finish(account string, h *helper, linked *signalv1.Device) (api.LoginEnd, error) {
	aci := linked.GetAci()
	if aci == "" {
		return api.LoginEnd{}, errors.New("signal: the phone's answer to linking named no account")
	}
	if other, err := a.linkedAs(aci, account); err != nil {
		return api.LoginEnd{}, err
	} else if other != "" {
		return api.LoginEnd{}, fmt.Errorf("signal: that Signal account is the account %s already", other)
	}
	if err := a.secrets.StoreSecret(linkRef(account), aci); err != nil {
		return api.LoginEnd{}, fmt.Errorf("signal: keep which Signal account %s is: %w", account, err)
	}
	h.add(aci)
	a.log.Info("linked", "account", account, "aci", aci)
	a.announceAll()
	return api.LoginEnd{Done: "linked to Signal as " + linked.GetNumber()}, nil
}

// linkedAs is the other account already linked to aci, or "".
func (a *Adapter) linkedAs(aci, except string) (string, error) {
	for _, account := range a.accountsNow() {
		if account.Name == except {
			continue
		}
		other, ok, err := a.linkedTo(account.Name)
		if err != nil {
			return "", err
		}
		if ok && other == aci {
			return account.Name, nil
		}
	}
	return "", nil
}

func (a *Adapter) configured(account string) bool {
	return slices.ContainsFunc(a.accountsNow(), func(acc Account) bool { return acc.Name == account })
}

// helperUp is the helper once it is up, waiting while it starts (or starts again).
func (a *Adapter) helperUp(ctx context.Context) (*helper, error) {
	for {
		a.mu.Lock()
		h, up, down := a.helper, a.up, a.down
		a.mu.Unlock()
		if h != nil {
			return h, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("signal: kith-signal is not up (%s): %w", down, ctx.Err())
		case <-up:
		}
	}
}

// setUp asks what to call a new account and has it written into the config.
func (a *Adapter) setUp(ctx context.Context, talk api.LoginTalk) (string, error) {
	accounts := a.accountsNow()
	taken := make([]string, 0, len(accounts))
	for _, acc := range accounts {
		taken = append(taken, acc.Name)
	}
	name, err := api.AskName(ctx, talk, domain.FreeName("phone", taken), taken,
		"What kith calls this Signal account: `kith login signal <name>` links it again.")
	if err != nil {
		return "", err
	}
	if err := talk.Configure(ctx, api.LoginRecord{Table: "signal.account", Values: map[string]string{"name": name}}); err != nil {
		return "", err
	}
	talk.Named(name)
	return name, nil
}
