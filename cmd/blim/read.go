package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/srg/blim/inspector"
	"github.com/srg/blim/internal/device"
)

// readCmd represents the read command
var readCmd = &cobra.Command{
	Use:   "read <device-address> [uuid]",
	Short: "Read a characteristic or descriptor value",
	Long: fmt.Sprintf(`Reads data from BLE characteristic(s) or a descriptor.

Examples:
  # Read Battery Level characteristic
  blim read %s 2a19

  # Read multiple characteristics (comma-separated)
  blim read %s 2a37,2a38,2a19 --hex

  # Read with service disambiguation
  blim read %s --service 180f --char 2a19

  # Read multiple characteristics from a specific service
  blim read %s --service 180d --char 2a37,2a38

  # Read descriptor (Client Characteristic Configuration)
  blim read %s --service 180d --char 2a37 --desc 2902

  # Output as hex
  blim read %s 2a19 --hex

  # Continuously watch characteristic (polls every second)
  blim read %s 2a37 --watch

  # Watch with custom interval
  blim read %s 2a37 --watch 500ms

%s`, exampleDeviceAddress, exampleDeviceAddress, exampleDeviceAddress, exampleDeviceAddress, exampleDeviceAddress, exampleDeviceAddress, exampleDeviceAddress, exampleDeviceAddress, deviceAddressNote),
	Args: cobra.RangeArgs(1, 2),
	RunE: runRead,
}

var (
	readServiceUUID string
	readCharUUIDs   string // supports comma-separated UUIDs
	readDescUUID    string
	readHex         bool
	readTimeout     time.Duration
	readWatch       string
)

func init() {
	readCmd.Flags().StringVar(&readServiceUUID, "service", "", "Service UUID (required if characteristic UUID is ambiguous)")
	readCmd.Flags().StringVar(&readCharUUIDs, "char", "", "Characteristic UUID(s), comma-separated for multiple")
	readCmd.Flags().StringVar(&readDescUUID, "desc", "", "Descriptor UUID (reads descriptor instead of characteristic)")
	readCmd.Flags().BoolVar(&readHex, "hex", false, "Output as hex string (e.g., 'FF01'); raw bytes by default")
	readCmd.Flags().DurationVar(&readTimeout, "timeout", 5*time.Second, "Read timeout")
	readCmd.Flags().StringVar(&readWatch, "watch", "", "Continuously read at interval (e.g., 1s, 500ms); default 1s if no value given")
	readCmd.Flags().Lookup("watch").NoOptDefVal = "1s"
}

func runRead(cmd *cobra.Command, args []string) error {
	address := args[0]

	// Determine UUID source (raw CSV string for later parsing)
	var uuidInput string
	if len(args) == 2 {
		uuidInput = args[1]
	} else if readCharUUIDs != "" {
		uuidInput = readCharUUIDs
	} else if readDescUUID != "" {
		uuidInput = readDescUUID
	} else {
		return fmt.Errorf("UUID required: provide as second argument or via --char/--desc flag")
	}

	// Parse for validation and routing
	charUUIDs := parseCSVUUIDs(uuidInput)
	if len(charUUIDs) == 0 {
		return fmt.Errorf("no valid UUIDs provided")
	}

	// Parse watch interval if watch flag is set
	var watchInterval time.Duration
	if readWatch != "" {
		// Watch mode: single characteristic only
		if len(charUUIDs) > 1 {
			return fmt.Errorf("watch mode requires a single characteristic, got %d", len(charUUIDs))
		}
		var err error
		watchInterval, err = time.ParseDuration(readWatch)
		if err != nil {
			return fmt.Errorf("invalid watch interval: %w", err)
		}
	}

	// Configure logger
	logger, err := configureLogger(cmd, "verbose")
	if err != nil {
		return err
	}

	// All arguments validated - don't show usage on runtime errors
	cmd.SilenceUsage = true

	// Setup progress description
	var progressDesc string
	operation := "Reading"
	if readWatch != "" {
		operation = "Watching"
	}
	if len(charUUIDs) == 1 {
		progressDesc = fmt.Sprintf("%s %s from %s", operation, charUUIDs[0], address)
	} else {
		progressDesc = fmt.Sprintf("%s %d characteristics from %s", operation, len(charUUIDs), address)
	}

	progress := NewProgressPrinter(progressDesc, "Connecting", "Processing")
	progress.Start()
	defer progress.Stop()

	// Build inspect options
	opts := &inspector.InspectOptions{
		ConnectTimeout:        30 * time.Second,
		DescriptorReadTimeout: readTimeout,
	}

	// Use background context
	ctx := context.Background()

	// Get output writer from command
	out := cmd.OutOrStdout()

	// Define the read operation
	readOperation := func(dev device.Device) (any, error) {
		// Stop progress indicator before printing output
		progress.Stop()

		// Get connection
		conn := dev.GetConnection()
		if conn == nil {
			return nil, fmt.Errorf("device not connected")
		}

		// Descriptor path
		if readDescUUID != "" {
			char, desc, _, err := resolveDescriptor(conn, readDescUUID, readServiceUUID, readCharUUIDs)
			if err != nil {
				return nil, err
			}
			return nil, performReadWithPrefix(out, char, desc, false)
		}

		// Characteristic path
		_, _, chars, err := resolveCharacteristics(conn, uuidInput, readServiceUUID)
		if err != nil {
			return nil, err
		}

		// Single characteristic
		if len(chars) == 1 {
			for _, char := range chars {
				if readWatch != "" {
					return nil, watchChar(out, ctx, dev, char, nil, watchInterval, logger)
				}
				return nil, performReadWithPrefix(out, char, nil, false)
			}
		}

		// Multi-characteristic
		return nil, performMultiRead(out, chars)
	}

	_, err = inspector.InspectDevice(ctx, address, opts, logger, progress.Callback(), readOperation)
	return err
}

// performMultiRead reads multiple characteristics and outputs with prefixes.
// UUIDs are sorted for deterministic output order.
func performMultiRead(w io.Writer, chars map[string]device.Characteristic) error {
	// Sort UUIDs for deterministic output
	charUUIDs := make([]string, 0, len(chars))
	for uuid := range chars {
		charUUIDs = append(charUUIDs, uuid)
	}
	sort.Strings(charUUIDs)

	for _, uuid := range charUUIDs {
		char := chars[uuid]
		data, err := char.Read(readTimeout)
		if err != nil {
			// Report error but continue with other characteristics
			fmt.Fprintf(os.Stderr, "%s: error: %v\n", device.ShortenUUID(uuid), err)
			continue
		}

		outputDataWithPrefix(w, uuid, data, true)
	}

	return nil
}

// performReadWithPrefix reads a single characteristic or descriptor with an optional UUID prefix.
func performReadWithPrefix(w io.Writer, char device.Characteristic, desc device.Descriptor, multiChar bool) error {
	var data []byte
	var err error

	// Read descriptor or characteristic
	if desc != nil {
		data, err = desc.Read(readTimeout)
		if err != nil {
			return fmt.Errorf("failed to read descriptor: %w", err)
		}
	} else {
		// Read characteristic using the abstracted interface
		data, err = char.Read(readTimeout)
		if err != nil {
			return fmt.Errorf("failed to read characteristic: %w", err)
		}
	}

	// Format and output data
	return outputData(w, data)
}

// watchChar continuously reads a characteristic or descriptor at the specified interval
func watchChar(w io.Writer, ctx context.Context, dev device.Device, char device.Characteristic, desc device.Descriptor, interval time.Duration, logger *logrus.Logger) error {
	fmt.Fprintf(os.Stderr, "Watching (reading every %v). Press Ctrl+C to stop...\n", interval)

	// Perform immediate first read
	if err := performSingleRead(w, char, desc, logger); err != nil {
		return err
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := performSingleRead(w, char, desc, logger); err != nil {
				// Check if the connection was lost by checking for ErrNotConnected in the error chain
				if errors.Is(err, device.ErrNotConnected) {
					return device.ErrConnectionLost
				}

				// Log other errors but continue watching
				logger.WithError(err).Warn("Failed to read characteristic, continuing...")
			} else {
				logger.Debug("Read operation successful")
			}
		}
	}
}

// performSingleRead executes a single read operation and outputs the data
func performSingleRead(w io.Writer, char device.Characteristic, desc device.Descriptor, logger *logrus.Logger) error {
	var data []byte
	var err error

	// Read descriptor or characteristic
	if desc != nil {
		data, err = desc.Read(readTimeout)
		if err != nil {
			logger.WithError(err).Error("failed to read descriptor")
			return err
		}
	} else {
		data, err = char.Read(readTimeout)
		if err != nil {
			logger.WithError(err).Error("failed to read characteristic")
			return err
		}
	}

	// Output data
	if err := outputData(w, data); err != nil {
		logger.WithError(err).Error("failed to output data")
		return err
	}

	return nil
}

// outputData formats and outputs data according to flags
func outputData(w io.Writer, data []byte) error {
	if readHex {
		// Hex output
		fmt.Fprintln(w, hex.EncodeToString(data))
		return nil
	}

	// Default: Raw binary output to writer
	_, err := w.Write(data)
	return err
}

// outputDataWithPrefix outputs data with an optional UUID prefix for multi-char reads.
func outputDataWithPrefix(w io.Writer, uuid string, data []byte, multiChar bool) {
	var prefix string
	if multiChar {
		prefix = device.ShortenUUID(uuid) + ": "
	}

	if readHex {
		fmt.Fprintf(w, "%s%s\n", prefix, hex.EncodeToString(data))
		return
	}

	// Raw binary output
	if prefix != "" {
		fmt.Fprint(w, prefix)
	}
	_, _ = w.Write(data)
	fmt.Fprintln(w)
}
