package cli

import "github.com/spf13/cobra"

func newPairCmd() *cobra.Command    { return &cobra.Command{Use: "pair", RunE: notYet("Pair")} }
func newDevicesCmd() *cobra.Command { return &cobra.Command{Use: "devices", RunE: notYet("Devices")} }
func newRemoteCmd() *cobra.Command  { return &cobra.Command{Use: "remote", RunE: notYet("Remote")} }
func newOpenCmd() *cobra.Command    { return &cobra.Command{Use: "open", RunE: notYet("Open")} }
