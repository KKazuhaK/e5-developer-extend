package main

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-renew/internal/catalog"
)

func (a *app) newCatalogCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "catalog", Short: "Inspect the built-in list of Graph calls"}
	var account string
	list := &cobra.Command{
		Use:   "list",
		Short: "List the read-only API calls the engine can make",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := a.ops().Catalog(account)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			defer tw.Flush()

			if account == "" {
				fmt.Fprintln(tw, "ID\tCATEGORY\tDEFAULT\tIDENTITIES\tNAME")
				for _, e := range res.Entries {
					api := e.API
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", api.ID, api.Category, yesNo(api.Default), identities(api), api.Name)
				}
				return nil
			}
			if res.NoGrants {
				fmt.Fprintf(out, "no granted permissions recorded for %q yet; run: e5renew run-now %s\n\n", res.Account.Name, res.Account.Name)
			}
			fmt.Fprintln(tw, "ID\tCATEGORY\tAVAILABLE\tNAME")
			for _, e := range res.Entries {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.API.ID, e.API.Category, e.Availability, e.API.Name)
			}
			return nil
		},
	}
	list.Flags().StringVar(&account, "account", "", "show which calls this account's granted permissions allow")
	cmd.AddCommand(list)
	return cmd
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
