package cli

import "github.com/spf13/cobra"

func newDaemonCmd() *cobra.Command { return &cobra.Command{Use: "daemon", RunE: notYet("Daemon")} }
func newStartCmd() *cobra.Command  { return &cobra.Command{Use: "start", RunE: notYet("Start")} }
func newStopCmd() *cobra.Command   { return &cobra.Command{Use: "stop", RunE: notYet("Stop")} }
func newStatusCmd() *cobra.Command { return &cobra.Command{Use: "status", RunE: notYet("Status")} }
