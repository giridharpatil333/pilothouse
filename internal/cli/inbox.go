package cli

import "github.com/spf13/cobra"

func newSetupCmd() *cobra.Command                     { return &cobra.Command{Use: "setup", RunE: notYet("Setup")} }
func newDoctorCmd() *cobra.Command                    { return &cobra.Command{Use: "doctor", RunE: notYet("Doctor")} }
func newInboxCmd() *cobra.Command                     { return &cobra.Command{Use: "inbox", RunE: notYet("Inbox")} }
func newLsCmd() *cobra.Command                        { return &cobra.Command{Use: "ls", RunE: notYet("Ls")} }
func newApproveCmd() *cobra.Command                   { return &cobra.Command{Use: "approve", RunE: notYet("Approve")} }
func newDenyCmd() *cobra.Command                      { return &cobra.Command{Use: "deny", RunE: notYet("Deny")} }
func newLogCmd() *cobra.Command                       { return &cobra.Command{Use: "log", RunE: notYet("Log")} }
func newExplainCmd() *cobra.Command                   { return &cobra.Command{Use: "explain", RunE: notYet("Explain")} }
func newServiceCmd() *cobra.Command                   { return &cobra.Command{Use: "service", RunE: notYet("Service")} }
func newNotifyCmd() *cobra.Command                    { return &cobra.Command{Use: "notify", RunE: notYet("Notify")} }
func newVersionCmd() *cobra.Command                   { return &cobra.Command{Use: "version", RunE: notYet("Version")} }
func newPresenceCmds() []*cobra.Command               { return nil }
func runDefault(cmd *cobra.Command, _ []string) error { return cmd.Help() }
