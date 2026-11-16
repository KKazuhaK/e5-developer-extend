package main

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-renew/internal/catalog"
	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/paths"
	"github.com/KKazuhaK/e5-renew/internal/store"
)

func (a *app) newCatalogCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "catalog", Short: "Inspect the built-in list of Graph calls"}
	var account string
	list := &cobra.Command{
		Use:   "list",
		Short: "List the read-only API calls the engine can make",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			apis, err := catalog.Load()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			defer tw.Flush()

			if account == "" {
				fmt.Fprintln(tw, "ID\tCATEGORY\tDEFAULT\tIDENTITIES\tNAME")
				for _, api := range apis {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", api.ID, api.Category, yesNo(api.Default), identities(api), api.Name)
				}
				return nil
			}

			cfg, _, err := a.loadConfig()
			if err != nil {
				return err
			}
			acct, err := a.findAccount(cfg, account)
			if err != nil {
				return err
			}
			dataDir, err := paths.DataDir()
			if err != nil {
				return err
			}
			state, _ := store.New(dataDir).LoadState()
			var granted []string
			if as := state.Accounts[acct.Name]; as != nil {
				granted = as.GrantedScopes
			}
			kind := catalog.Delegated
			if !acct.Delegated() {
				kind = catalog.App
			}
			if len(granted) == 0 {
				fmt.Fprintf(out, "no granted permissions recorded for %q yet; run: e5renew run-now %s\n\n", acct.Name, acct.Name)
			}
			usable := map[string]bool{}
			for _, api := range catalog.Available(apis, kind, granted, acct.Categories) {
				usable[api.ID] = true
			}
			fmt.Fprintln(tw, "ID\tCATEGORY\tAVAILABLE\tNAME")
			for _, api := range apis {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", api.ID, api.Category, availability(api, kind, granted, *acct, usable[api.ID]), api.Name)
			}
			return nil
		},
	}
	list.Flags().StringVar(&account, "account", "", "show which calls this account's granted permissions allow")
	cmd.AddCommand(list)
	return cmd
}

func availability(api catalog.API, kind string, granted []string, acct config.Account, usable bool) string {
	switch {
	case usable:
		return "yes"
	case api.Path(kind) == "":
		return "no (not supported for this identity)"
	}
	if len(acct.Categories) > 0 {
		found := false
		for _, c := range acct.Categories {
			found = found || strings.EqualFold(c, api.Category)
		}
		if !found {
			return "no (category not selected)"
		}
	} else if !api.Default {
		return "no (off by default; select its category)"
	}
	return "no (missing permission)"
}

func identities(api catalog.API) string {
	var parts []string
	if api.Delegated != "" {
		parts = append(parts, "delegated")
	}
	if api.App != "" {
		parts = append(parts, "app")
	}
	return strings.Join(parts, ",")
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
