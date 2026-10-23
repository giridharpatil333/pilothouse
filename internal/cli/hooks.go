package cli

import "github.com/spf13/cobra"

func newHooksCmd() *cobra.Command { return &cobra.Command{Use: "hooks", RunE: notYet("Hooks")} }
func newHookShimCmd() *cobra.Command {
	return &cobra.Command{Use: "hook", Hidden: true, RunE: notYet("hook")}
}
