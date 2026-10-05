package daemon

import (
	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
)

// A login's steps and networks on the wire.

func loginStepToProto(s api.LoginStep) *v1.LoginStep {
	out := &v1.LoginStep{Login: s.Login, Account: s.Account, Note: s.Note, Code: s.Code, Restart: s.Restart, Done: s.Done}
	for _, f := range s.Ask {
		out.Ask = append(out.Ask, &v1.LoginField{Key: f.Key, Label: f.Label, Help: f.Help, Value: f.Value, Secret: f.Secret, Optional: f.Optional})
	}
	if s.Configure != nil {
		out.Configure = &v1.LoginRecord{Table: s.Configure.Table, Values: s.Configure.Values}
	}
	return out
}

func loginStepFromProto(s *v1.LoginStep) api.LoginStep {
	out := api.LoginStep{
		Login: s.GetLogin(), Account: s.GetAccount(), Note: s.GetNote(), Code: s.GetCode(),
		Restart: s.GetRestart(), Done: s.GetDone(),
	}
	for _, f := range s.GetAsk() {
		out.Ask = append(out.Ask, api.LoginField{
			Key: f.GetKey(), Label: f.GetLabel(), Help: f.GetHelp(), Value: f.GetValue(), Secret: f.GetSecret(), Optional: f.GetOptional(),
		})
	}
	if c := s.GetConfigure(); c != nil {
		out.Configure = &api.LoginRecord{Table: c.GetTable(), Values: c.GetValues()}
	}
	return out
}

func loginNetworksToProto(networks []api.LoginNetwork) []*v1.LoginNetwork {
	out := make([]*v1.LoginNetwork, 0, len(networks))
	for _, n := range networks {
		pn := &v1.LoginNetwork{Network: n.Network, Label: n.Label, Detail: n.Detail}
		for _, a := range n.Accounts {
			pn.Accounts = append(pn.Accounts, &v1.LoginAccount{Name: a.Name, Detail: a.Detail})
		}
		out = append(out, pn)
	}
	return out
}

func loginNetworksFromProto(networks []*v1.LoginNetwork) []api.LoginNetwork {
	out := make([]api.LoginNetwork, 0, len(networks))
	for _, n := range networks {
		an := api.LoginNetwork{Network: n.GetNetwork(), Label: n.GetLabel(), Detail: n.GetDetail()}
		for _, a := range n.GetAccounts() {
			an.Accounts = append(an.Accounts, api.LoginAccount{Name: a.GetName(), Detail: a.GetDetail()})
		}
		out = append(out, an)
	}
	return out
}
