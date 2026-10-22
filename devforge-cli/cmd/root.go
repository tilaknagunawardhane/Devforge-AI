package cmd

import (
	"fmt"
	"os"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "devforge",
	Short: "DevForge AI - The Robot Platform Engineer",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Welcome to DevForge AI. Use 'devforge --help' to see available commands.")
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}