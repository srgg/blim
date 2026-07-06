package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"unicode"

	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// formatVersion adds 'v' prefix if version starts with a digit
func formatVersion(ver string) string {
	if len(ver) > 0 && unicode.IsDigit(rune(ver[0])) {
		return "v" + ver
	}
	return ver
}

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "blim",
	Short: "Bluetooth Low Energy CLI tool",
	Long: `Bluetooth Low Energy (BLE) command-line tool that provides:

- Scan and discover nearby BLE devices
- Inspect GATT services, characteristics, and descriptors
- Read from and write to characteristics
- Monitor characteristic changes via notifications
- Bridge BLE devices to PTY for serial-like access
- Lua scripting API for advanced automation and protocol handling; see bridge and inspect commands.

Ideal for firmware development, automated testing, and BLE protocols exploration.`,
	// version, commit, and date are injected at release time via -ldflags -X
	// (see .github/workflows/release.yaml and the Makefile).
	Version: fmt.Sprintf("%s (commit %s, built %s)", formatVersion(version), commit, date),
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		// Ctrl+C is a normal exit, not an error - exit silently
		if errors.Is(err, context.Canceled) {
			return
		}
		// Print user-friendly error message
		fmt.Fprintf(os.Stderr, "ERROR: %s\n", FormatUserError(err))
		os.Exit(1)
	}
}

func init() {
	// Silence Cobra's "Error:" prefix - main() prints clean errors
	rootCmd.SilenceErrors = true

	// Add subcommands
	rootCmd.AddCommand(scanCmd)
	rootCmd.AddCommand(bridgeCmd)
	rootCmd.AddCommand(inspectCmd)
	rootCmd.AddCommand(readCmd)
	rootCmd.AddCommand(writeCmd)
	rootCmd.AddCommand(subscribeCmd)

	// Global flags
	rootCmd.PersistentFlags().String("log-level", "", "Log level (debug, info, warn, error)")

	// Add -v as a short flag for --version
	rootCmd.Flags().BoolP("version", "v", false, "Show version information")
}
