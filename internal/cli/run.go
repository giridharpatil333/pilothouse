package cli

import "github.com/spf13/cobra"

func newRunCmd() *cobra.Command    { return &cobra.Command{Use: "run", RunE: notYet("Run")} }
func newAttachCmd() *cobra.Command { return &cobra.Command{Use: "attach", RunE: notYet("Attach")} }
func newTaskCmd() *cobra.Command   { return &cobra.Command{Use: "task", RunE: notYet("Task")} }
func newHolderCmd() *cobra.Command {
	return &cobra.Command{Use: "__holder", Hidden: true, RunE: notYet("holder")}
}
