package cli

import "github.com/spf13/cobra"

func newPolicyCmd() *cobra.Command { return &cobra.Command{Use: "policy", RunE: notYet("Policy")} }
