package slack

import (
	"context"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// sessionWarning is what the copied session is, said before it is asked for.
const sessionWarning = "Both are a session: whoever has them can read and write as you. kith keeps them in " +
	"the system keyring. Signing out of Slack in that browser ends this session too."

// LoginNetwork is Slack as a login lists it, with its workspaces.
func (a *Adapter) LoginNetwork() api.LoginNetwork {
	n := api.LoginNetwork{Network: "slack", Label: "Slack", Detail: "a workspace, signed in with your browser's session"}
	for _, account := range a.accountsNow() {
		n.Accounts = append(n.Accounts, api.LoginAccount{Name: account.Name, Detail: account.Workspace})
	}
	return n
}

// Login signs account in with the session a browser signed in to its workspace holds,
// setting it up first when it is new (""): Slack checks the session, and a refused
// one is asked for again.
func (a *Adapter) Login(ctx context.Context, account string, talk api.LoginTalk) (api.LoginEnd, error) {
	if account == "" {
		name, err := a.setUp(ctx, talk)
		if err != nil {
			return api.LoginEnd{}, err
		}
		account = name
	}
	workspace := ""
	for _, acc := range a.accountsNow() {
		if acc.Name == account {
			workspace = acc.Workspace
		}
	}
	help := sessionHelp(workspace)
	note := ""
	for {
		got, err := talk.Ask(ctx, note,
			api.LoginField{Key: "token", Label: "token (xoxc-…)", Secret: true, Help: "kith signs in with the session your " +
				"browser holds. In a browser signed in to " + help.where + ", open " + help.open + ", then the " +
				"developer tools (F12) → Console, and paste this line; it prints the token, which starts with xoxc-:" +
				"\n\n    " + help.token + "\n\n" + sessionWarning},
			api.LoginField{Key: "cookie", Label: "cookie d (xoxd-…)", Secret: true, Help: "In the same developer tools: " +
				help.cookie + ", which starts with xoxd-. Copied as it shows or decoded, either works.\n\n" + sessionWarning})
		if err != nil {
			return api.LoginEnd{}, err
		}
		token, cookie := strings.TrimSpace(got["token"]), strings.TrimSpace(got["cookie"])
		switch {
		case !strings.HasPrefix(token, "xoxc-"):
			note = "the token starts with xoxc- — copy it from the console line"
			continue
		case !strings.HasPrefix(cookie, "xoxd-"):
			note = "the cookie d starts with xoxd- — copy its value from the cookies of https://app.slack.com"
			continue
		}
		in, err := a.signIn(ctx, account, token, cookie)
		if err != nil {
			if ctx.Err() != nil {
				return api.LoginEnd{}, err
			}
			note = err.Error()
			continue
		}
		return api.LoginEnd{Done: "signed in to " + in.workspace + " as " + in.user}, nil
	}
}

// setUp asks for a new workspace and what to call it, and has it written into the
// config.
func (a *Adapter) setUp(ctx context.Context, talk api.LoginTalk) (string, error) {
	accounts := a.accountsNow()
	var address, note string
	for {
		got, err := talk.Ask(ctx, note, api.LoginField{Key: "workspace", Label: "workspace", Help: "The workspace: its " +
			"address — acme, for acme.slack.com — or, with Slack open in a browser, the link in its address bar " +
			"(https://app.slack.com/client/T…/…), pasted whole."})
		if err != nil {
			return "", err
		}
		address = addressOf(got["workspace"])
		if err := checkAddress(address); err != nil {
			note = err.Error()
			continue
		}
		if i := slices.IndexFunc(accounts, func(acc Account) bool { return acc.Workspace == address }); i >= 0 {
			note = "that workspace is the account " + accounts[i].Name + " already; sign it in from the list"
			continue
		}
		break
	}
	taken := make([]string, 0, len(accounts))
	for _, acc := range accounts {
		taken = append(taken, acc.Name)
	}
	suggested := address
	if isTeamID(address) {
		suggested = "work" // an ID says nothing
	}
	name, err := api.AskName(ctx, talk, domain.FreeName(suggested, taken), taken,
		"What kith calls this workspace: `kith login slack <name>` signs it in again. Suggested from its "+
			"address; enter keeps it.")
	if err != nil {
		return "", err
	}
	if err := talk.Configure(ctx, api.LoginRecord{Table: "slack.account", Values: map[string]string{"name": name, "workspace": address}}); err != nil {
		return "", err
	}
	talk.Named(name)
	return name, nil
}

// session is where a workspace's session is copied from: open, the page to open in a
// browser signed in to it; token and cookie, how to find each half there.
type session struct {
	where, open, token, cookie string
}

// sessionHelp is the help for a workspace, by address ("acme") or team ID.
func sessionHelp(workspace string) session {
	h := session{
		where: workspace + ".slack.com", open: "https://" + workspace + ".slack.com",
		token: `Object.values(JSON.parse(localStorage.localConfig_v2).teams).find(t => t.url.includes("//` + workspace + `.")).token`,
	}
	if isTeamID(workspace) {
		h.where, h.open = "workspace "+workspace, "https://app.slack.com/client/"+workspace
		h.token = `JSON.parse(localStorage.localConfig_v2).teams["` + workspace + `"].token`
	}
	h.cookie = "Application (Storage in Firefox) → Cookies → https://app.slack.com — the value of the cookie named d"
	return h
}
