package ops

import (
	"context"
	"errors"
	"fmt"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

// RenameAccount gives an account a new name. Only config.toml changes: the
// state, the vault entries, the token cache, the trigger files and the call log
// all hang on the account's key (config.Account.Key), which a rename keeps. An
// account that has no ID yet gets one that equals its old name, which is the
// key everything stored for it already has.
//
// The running engine picks the new name up when it reloads the configuration;
// call ReloadEngine afterwards. A rename of an account that is not in the
// configuration (including the same rename a second time) fails.
func (o *Ops) RenameAccount(ctx context.Context, from, to string) error {
	if from == to {
		return errors.New("the new name is the same as the old one")
	}
	unlock, err := o.LockConfig(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	cfg, path, err := o.LoadConfig()
	if err != nil {
		return err
	}
	cur, err := o.FindAccount(cfg, from)
	if err != nil {
		return err
	}
	if _, exists := cfg.Find(to); exists {
		return fmt.Errorf("account %q already exists", to)
	}
	renamed := *cur
	renamed.Name = to
	if err := ValidateAccount(renamed); err != nil {
		return err
	}
	if cur.ID == "" {
		cur.ID = cur.Name // the key stays what it was
	}
	cur.Name = to
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid account:\n%w", err)
	}
	save := o.SaveConfig
	if save == nil {
		save = func(c *config.Config, p string) error { return c.Save(p) }
	}
	if err := save(cfg, path); err != nil {
		return fmt.Errorf("save config.toml: %w", err)
	}
	return nil
}
