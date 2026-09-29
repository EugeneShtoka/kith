package setup

import (
	"errors"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// CodeRules builds the code shape from config, rejecting a set of rules that could
// never match anything — a configuration mistake rather than a preference, because
// finding no codes ever is indistinguishable from the feature being broken.
func CodeRules(codes config.Codes) (domain.CodeRules, error) {
	rules := domain.CodeRules{
		MinLength:    codes.Min(),
		MaxLength:    codes.Max(),
		Letters:      codes.LettersAllowed(),
		Digits:       codes.DigitsAllowed(),
		Symbols:      codes.Symbols,
		RequireDigit: codes.DigitRequired(),
	}
	if err := rules.Validate(); err != nil {
		return domain.CodeRules{}, fmt.Errorf("codes: %w", err)
	}
	return rules, nil
}

// CodeScope is where kith looks for codes on its own initiative — the include and
// exclude lists, unchanged.
func CodeScope(codes config.Codes) domain.CodeScope {
	return domain.CodeScope{Include: codes.Include, Exclude: codes.Exclude}
}

// Codes derives both halves — the shape and the scope — from a configuration.
func Codes(codes config.Codes) (domain.CodeRules, domain.CodeScope, error) {
	rules, err := CodeRules(codes)
	if err != nil {
		return domain.CodeRules{}, domain.CodeScope{}, err
	}
	return rules, CodeScope(codes), nil
}

// AutoCopy derives the unattended-capture decision from a configuration.
func AutoCopy(cfg config.Config) (domain.AutoCopy, error) {
	rules, scope, err := Codes(cfg.Codes)
	if err != nil {
		return domain.AutoCopy{}, err
	}
	return domain.AutoCopy{Enabled: cfg.Clipboard.AutoCopy, Scope: scope, Rules: rules}, nil
}

// AutoCopyUnavailable reports why auto-copy cannot work as configured, or nil when it
// can.
func AutoCopyUnavailable(cfg config.Config) error {
	if !cfg.Clipboard.AutoCopy {
		return nil
	}
	if cfg.Clipboard.Command == "" {
		return errors.New(
			"no `[clipboard] command` is set, and a daemon has no terminal to copy through — " +
				"set one (wl-copy, pbcopy, xclip -selection clipboard)")
	}
	if len(cfg.Codes.Include) == 0 {
		return errors.New(
			"`[codes] include` names nowhere, which means everywhere — " +
				"unattended copying needs a named place, or anyone who can message you can rewrite your clipboard")
	}
	return nil
}
