package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/srgg/blim/inspector"
	"github.com/srgg/blim/internal/device"
)

// writeCmd represents the write command
var writeCmd = &cobra.Command{
	Use:   "write <device-address> <uuid> <data>",
	Short: "Write to a characteristic or descriptor",
	Long: fmt.Sprintf(`Writes data to a BLE characteristic or descriptor.

Examples:
  # Write to characteristic (string data)
  blim write %s 2a06 "high"

  # Write hex data
  blim write %s 2a06 01 --hex

  # Write to descriptor (enable notifications)
  blim write %s --service 180d --char 2a37 --desc 2902 0100 --hex

  # Write without response (faster, no ACK)
  blim write %s 2a06 "data" --without-response

%s`, exampleDeviceAddress, exampleDeviceAddress, exampleDeviceAddress, exampleDeviceAddress, deviceAddressNote),
	Args: cobra.RangeArgs(2, 3),
	RunE: runWrite,
}

var (
	writeServiceUUID string
	writeCharUUID    string
	writeDescUUID    string
	writeHex         bool
	writeNoResponse  bool
	writeChunkSize   int
	writeTimeout     time.Duration
)

func init() {
	writeCmd.Flags().StringVar(&writeServiceUUID, "service", "", "Service UUID (required if characteristic UUID is ambiguous)")
	writeCmd.Flags().StringVar(&writeCharUUID, "char", "", "Characteristic UUID")
	writeCmd.Flags().StringVar(&writeDescUUID, "desc", "", "Descriptor UUID (writes descriptor instead of characteristic)")
	writeCmd.Flags().BoolVar(&writeHex, "hex", false, "Parse input as hex string (e.g., 'FF01'); raw bytes by default")
	writeCmd.Flags().BoolVar(&writeNoResponse, "without-response", false, "Write without response (faster, no ACK); default waits for ACK, if available")
	writeCmd.Flags().IntVar(&writeChunkSize, "chunk", 0, "Force writes into N-byte chunks; default 0, auto-detect from MTU")
	writeCmd.Flags().DurationVar(&writeTimeout, "timeout", 5*time.Second, "Write timeout")
}

func runWrite(cmd *cobra.Command, args []string) error {
	address := args[0]

	// Parse UUID from positional arg or flags
	var targetUUID string
	if len(args) >= 2 {
		targetUUID = args[1]
	} else if writeCharUUID != "" {
		targetUUID = writeCharUUID
	} else if writeDescUUID != "" {
		targetUUID = writeDescUUID
	} else {
		return fmt.Errorf("UUID required: provide as second argument or via --char/--desc flag")
	}

	// Parse data from positional arg
	if len(args) < 3 {
		return fmt.Errorf("data required: provide as third argument")
	}
	dataStr := args[2]

	// Parse data according to format
	data, err := parseWriteData(dataStr)
	if err != nil {
		return fmt.Errorf("failed to parse data: %w", err)
	}

	// Configure logger
	logger, err := configureLogger(cmd, "verbose")
	if err != nil {
		return err
	}

	// All arguments validated - don't show usage on runtime errors
	cmd.SilenceUsage = true

	// Setup progress printer
	progress := NewProgressPrinter(fmt.Sprintf("Writing %d bytes to %s on %s", len(data), targetUUID, address), "Connecting", "Processing")
	progress.Start()
	defer progress.Stop()

	// Build inspect options
	opts := &inspector.InspectOptions{
		ConnectTimeout:        30 * time.Second,
		DescriptorReadTimeout: 0, // Skip descriptor reads for write operations
	}

	// Use background context
	ctx := context.Background()

	// Define the write operation
	writeOperation := func(dev device.Device) (any, error) {
		// Stop progress indicator before returning
		progress.Stop()

		// Get connection
		conn := dev.GetConnection()
		if conn == nil {
			return nil, fmt.Errorf("device not connected")
		}

		// Resolve target characteristic/descriptor
		char, desc, _, err := doResolveTarget(conn, targetUUID, writeServiceUUID, writeCharUUID, writeDescUUID)
		if err != nil {
			return nil, err
		}

		// Perform write
		return nil, performWrite(dev, char, desc, data)
	}

	_, err = inspector.InspectDevice(ctx, address, opts, logger, progress.Callback(), writeOperation)
	if err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Write successful")
	return nil
}

// parseWriteData converts input string to bytes based on format flags
func parseWriteData(dataStr string) ([]byte, error) {
	if writeHex {
		// Remove spaces and common separators
		cleaned := strings.ReplaceAll(dataStr, " ", "")
		cleaned = strings.ReplaceAll(cleaned, ":", "")
		cleaned = strings.ReplaceAll(cleaned, "-", "")
		cleaned = strings.ReplaceAll(cleaned, "0x", "")

		data, err := hex.DecodeString(cleaned)
		if err != nil {
			return nil, fmt.Errorf("invalid hex data: %w", err)
		}
		return data, nil
	}

	return []byte(dataStr), nil
}

// performWrite executes a write operation on a characteristic or descriptor
func performWrite(dev device.Device, char device.Characteristic, desc device.Descriptor, data []byte) error {
	// Write to descriptor or characteristic
	if desc != nil {
		return writeDescriptor(dev, char, desc, data)
	}

	return writeCharacteristic(dev, char, data)
}

// writeCharacteristic writes data to a characteristic
func writeCharacteristic(dev device.Device, char device.Characteristic, data []byte) error {
	// Check write properties
	props := char.GetProperties()
	if props == nil {
		return fmt.Errorf("characteristic %s: properties not available", char.UUID())
	}

	writeProps := props.Write()
	writeNoRespProps := props.WriteWithoutResponse()

	canWrite := writeProps != nil && writeProps.Value() != 0
	canWriteNoResponse := writeNoRespProps != nil && writeNoRespProps.Value() != 0

	if !canWrite && !canWriteNoResponse {
		return fmt.Errorf("characteristic %s does not support write operations", char.UUID())
	}

	// Determine write mode: defaults to with-response when supported
	// Use without-response only if explicitly requested via --without-response flag
	withResponse := !writeNoResponse && canWrite

	// Perform write using the abstracted interface
	err := char.Write(data, withResponse, writeTimeout)
	if err != nil {
		return fmt.Errorf("failed to write characteristic: %w", err)
	}

	return nil
}

// writeDescriptor writes data to a descriptor
// NOTE: Descriptor writes are deferred to future implementation.
// Most common descriptor writes (like CCCD for notifications) are handled automatically
// by the Connection.Subscribe() mechanism.
func writeDescriptor(dev device.Device, char device.Characteristic, desc device.Descriptor, data []byte) error {
	return fmt.Errorf("descriptor writes not yet implemented (deferred to future version)")
}
