package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/srg/blim/inspector"
	"github.com/srg/blim/internal/device"
)

// subscribeCmd represents the subscribe command
var subscribeCmd = &cobra.Command{
	Use:   "subscribe <device-address> [uuid]",
	Short: "Subscribe to characteristic notifications",
	Long: fmt.Sprintf(`Subscribes to BLE characteristic notifications and outputs received data.

Stream modes:
  live     - Output every notification immediately (default)
  batched  - Collect notifications, output at rate interval
  latest   - Keep only latest value per characteristic, output at rate interval

Examples:
  # Subscribe to single characteristic
  blim subscribe %s 2a37

  # Subscribe to multiple characteristics (auto-resolves services)
  blim subscribe %s 2A6E,2A6F,2A19 --hex

  # Subscribe to characteristics in specific service
  blim subscribe %s --service 180d --char 2a37,2a38

  # Subscribe to all notifiable characteristics in service
  blim subscribe %s --service ff30

  # Batched mode with 1s collection window
  blim subscribe %s --service ff30 --char ff31,ff32 --mode batched --rate 1s

%s`, exampleDeviceAddress, exampleDeviceAddress, exampleDeviceAddress, exampleDeviceAddress, exampleDeviceAddress, deviceAddressNote),
	Args: cobra.RangeArgs(1, 2),
	RunE: runSubscribe,
}

var (
	subscribeServiceUUID string
	subscribeCharUUIDs   string // comma-separated
	subscribeHex         bool
	subscribeTimeout     time.Duration
	subscribeMode        string
	subscribeRate        time.Duration
	subscribeIndicate    bool
)

func init() {
	subscribeCmd.Flags().StringVar(&subscribeServiceUUID, "service", "", "Service UUID (optional; auto-resolves if omitted)")
	subscribeCmd.Flags().StringVar(&subscribeCharUUIDs, "char", "", "Characteristic UUID(s), comma-separated (e.g., 2a37,2a38)")
	subscribeCmd.Flags().BoolVar(&subscribeHex, "hex", false, "Output as hex string; raw bytes by default")
	subscribeCmd.Flags().DurationVar(&subscribeTimeout, "timeout", 30*time.Second, "Connection timeout")
	subscribeCmd.Flags().StringVar(&subscribeMode, "mode", "live", "Stream mode: live, batched, or latest")
	subscribeCmd.Flags().DurationVar(&subscribeRate, "rate", 1*time.Second, "Rate limit interval for batched/latest modes")
	subscribeCmd.Flags().BoolVar(&subscribeIndicate, "indicate", false, "Use indications instead of notifications")
}

// parseStreamMode converts CLI mode string to device.StreamMode
func parseStreamMode(mode string) (device.StreamMode, error) {
	switch strings.ToLower(mode) {
	case "live", "instant", "every":
		return device.StreamEveryUpdate, nil
	case "batched", "batch":
		return device.StreamBatched, nil
	case "latest", "aggregated":
		return device.StreamAggregated, nil
	default:
		return 0, fmt.Errorf("invalid mode %q: use live, batched, or latest", mode)
	}
}

// buildSubscribeOptions resolves characteristics and groups them by service.
// charUUIDsCSV is a comma-separated string of characteristic UUIDs (parsed internally).
// If charUUIDsCSV is empty and serviceUUID is provided, returns all notifiable chars in that service.
// Returns a map of service UUID to characteristic UUIDs, and total characteristic count.
func buildSubscribeOptions(conn device.Connection, charUUIDsCSV, serviceUUID string) (map[string][]string, int, error) {
	serviceChars, _, chars, err := resolveCharacteristics(conn, charUUIDsCSV, serviceUUID)
	if err != nil {
		return nil, 0, err
	}

	// Filter to only notifiable characteristics
	filteredServiceChars := make(map[string][]string)
	filteredCount := 0

	for svcUUID, charUUIDs := range serviceChars {
		for _, charUUID := range charUUIDs {
			char := chars[charUUID]
			if supportsNotifications(char) {
				filteredServiceChars[svcUUID] = append(filteredServiceChars[svcUUID], charUUID)
				filteredCount++
			} else if charUUIDsCSV != "" {
				// Explicit char requested but doesn't support notifications
				return nil, 0, fmt.Errorf("characteristic %s does not support notifications", device.ShortenUUID(charUUID))
			}
			// If charUUIDsCSV == "" (all-in-service mode), silently skip non-notifiable
		}
	}

	if filteredCount == 0 {
		return nil, 0, fmt.Errorf("no notifiable characteristics found")
	}

	return filteredServiceChars, filteredCount, nil
}

func runSubscribe(cmd *cobra.Command, args []string) error {
	address := args[0]

	// Parse stream mode
	streamMode, err := parseStreamMode(subscribeMode)
	if err != nil {
		return err
	}

	// Determine characteristics to subscribe (raw CSV string for later parsing)
	var charUUIDsCSV string
	if len(args) == 2 {
		charUUIDsCSV = args[1]
	} else if subscribeCharUUIDs != "" {
		charUUIDsCSV = subscribeCharUUIDs
	}
	// If no chars specified, we'll subscribe to all in service (requires --service)

	// Validate: either chars specified or service specified (for all-in-service mode)
	if charUUIDsCSV == "" && subscribeServiceUUID == "" {
		return fmt.Errorf("specify characteristic UUID(s) via argument or --char flag, or use --service for all characteristics")
	}

	// Configure logger
	logger, err := configureLogger(cmd, "verbose")
	if err != nil {
		return err
	}

	// All arguments validated - don't show usage on runtime errors
	cmd.SilenceUsage = true

	// Setup context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	go func() {
		<-sigChan
		cancel()
	}()

	// Setup progress (detailed description comes after resolution)
	progress := NewProgressPrinter(fmt.Sprintf("Subscribing to %s", address), "Connecting", "Subscribed")
	progress.Start()
	defer progress.Stop()

	// Build inspect options
	opts := &inspector.InspectOptions{
		ConnectTimeout:        subscribeTimeout,
		DescriptorReadTimeout: 2 * time.Second,
	}

	// Track if we're subscribing to multiple characteristics (for output formatting)
	var multiChar bool

	// Define the subscribe operation
	subscribeOperation := func(dev device.Device) (any, error) {
		conn := dev.GetConnection()
		if conn == nil {
			return nil, fmt.Errorf("device not connected")
		}

		// Build subscription options - group characteristics by service
		serviceChars, totalChars, err := buildSubscribeOptions(conn, charUUIDsCSV, subscribeServiceUUID)
		if err != nil {
			return nil, err
		}

		multiChar = totalChars > 1

		// Stop progress indicator
		progress.Stop()

		// Print subscription info
		if len(serviceChars) == 1 {
			// Single service
			for svcUUID, chars := range serviceChars {
				if len(chars) == 1 {
					fmt.Fprintf(os.Stderr, "Subscribed to %s. Press Ctrl+C to stop...\n", chars[0])
				} else {
					fmt.Fprintf(os.Stderr, "Subscribed to %d characteristics in service %s. Press Ctrl+C to stop...\n", len(chars), svcUUID)
				}
			}
		} else {
			// Multiple services
			fmt.Fprintf(os.Stderr, "Subscribed to %d characteristics across %d services. Press Ctrl+C to stop...\n", totalChars, len(serviceChars))
		}

		// Build SubscribeOptions for each service
		var subscribeOpts []*device.SubscribeOptions
		for svcUUID, chars := range serviceChars {
			subscribeOpts = append(subscribeOpts, &device.SubscribeOptions{
				Service:         svcUUID,
				Characteristics: chars,
				Indicate:        subscribeIndicate,
			})
		}

		// Determine rate for subscription
		rate := subscribeRate
		if streamMode == device.StreamEveryUpdate {
			rate = 0 // No rate limiting for live mode
		}

		// Subscribe
		_, err = conn.Subscribe(
			subscribeOpts,
			streamMode,
			rate,
			func(record *device.Record) {
				outputSubscribeRecord(record, multiChar)
			},
		)
		if err != nil {
			return nil, fmt.Errorf("failed to subscribe: %w", err)
		}

		// Wait for either user cancellation (Ctrl+C) or connection loss
		connCtx := conn.ConnectionContext()
		select {
		case <-ctx.Done():
			// User cancelled
			return nil, nil
		case <-connCtx.Done():
			// Connection lost
			return nil, ErrConnectionLost
		}
	}

	_, err = inspector.InspectDevice(ctx, address, opts, logger, progress.Callback(), subscribeOperation)
	return err
}

// outputSubscribeRecord formats and outputs a subscription record.
// Keys are sorted for deterministic output order.
func outputSubscribeRecord(record *device.Record, multiChar bool) {
	printValue := func(charUUID string, data []byte) {
		var prefix string
		if multiChar {
			prefix = device.ShortenUUID(charUUID) + ": "
		}

		if subscribeHex {
			fmt.Printf("%s%s\n", prefix, hex.EncodeToString(data))
		} else {
			if prefix != "" {
				fmt.Print(prefix)
			}
			_, _ = os.Stdout.Write(data)
			fmt.Println()
		}
	}

	// Handle batched mode (BatchValues)
	if record.BatchValues != nil {
		charUUIDs := make([]string, 0, len(record.BatchValues))
		for k := range record.BatchValues {
			charUUIDs = append(charUUIDs, k)
		}
		sort.Strings(charUUIDs)

		for _, charUUID := range charUUIDs {
			for _, data := range record.BatchValues[charUUID] {
				printValue(charUUID, data)
			}
		}
		return
	}

	// Handle live/latest mode (Values)
	charUUIDs := make([]string, 0, len(record.Values))
	for k := range record.Values {
		charUUIDs = append(charUUIDs, k)
	}
	sort.Strings(charUUIDs)

	for _, charUUID := range charUUIDs {
		printValue(charUUID, record.Values[charUUID])
	}
}

// supportsNotifications checks if a characteristic supports notifications or indications
func supportsNotifications(char device.Characteristic) bool {
	props := char.GetProperties()
	supportsNotify := props.Notify() != nil && props.Notify().Value() != 0
	supportsIndicate := props.Indicate() != nil && props.Indicate().Value() != 0
	return supportsNotify || supportsIndicate
}
