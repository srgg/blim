package goble

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/sirupsen/logrus"
	"github.com/srgg/blim/internal/device"
)

const (
	// DefaultBLEWriteChunkSize is the maximum number of bytes to write in a single BLE operation.
	// BLE 4.0/4.1 spec defines ATT_MTU of 23 bytes (20 bytes payload after ATT header overhead).
	// Keeping chunks at 20 bytes ensures compatibility with all BLE versions.
	DefaultBLEWriteChunkSize = 20

	// DefaultBLEWriteDelay is the delay between consecutive write chunks.
	// This prevents overwhelming the BLE peripheral's receive buffer and ensures reliable delivery.
	DefaultBLEWriteDelay = 10 * time.Millisecond
)

// BLEAdvertisedService implements the Service interface for advertised services
type BLEAdvertisedService struct {
	uuid            string
	characteristics []device.Characteristic
}

func (s *BLEAdvertisedService) UUID() string {
	return s.uuid
}

func (s *BLEAdvertisedService) KnownName() string {
	return "" // Advertised services don't have known names until connected
}

func (s *BLEAdvertisedService) GetCharacteristics() []device.Characteristic {
	return s.characteristics
}

// BLEDevice implements the Device interface for BLE devices
type BLEDevice struct {
	// Device data
	id                 string
	name               string
	address            string
	rssi               int
	txPower            *int
	connectable        bool
	lastSeen           time.Time
	advertisedServices []string
	manufData          []byte
	parsedManufData    interface{}
	serviceData        map[string][]byte
	connection         *BLEConnection
	onData             func(uuid string, data []byte)
	logger             *logrus.Logger
	mu                 sync.RWMutex
}

// NewBLEDevice creates a BLEDevice with a pre-created connection instance
func NewBLEDevice(address string, logger *logrus.Logger) *BLEDevice {
	if logger == nil {
		logger = logrus.New()
	}

	return &BLEDevice{
		id:                 address,
		address:            address,
		advertisedServices: make([]string, 0),
		serviceData:        make(map[string][]byte),
		lastSeen:           time.Now(),
		connection:         NewBLEConnection(logger),
		logger:             logger,
	}
}

// NewBLEDeviceFromAdvertisement creates a BLEDevice from a device.Advertisement
func NewBLEDeviceFromAdvertisement(adv device.Advertisement, logger *logrus.Logger) *BLEDevice {
	// Use the new constructor with preconnection
	dev := NewBLEDevice(adv.Addr(), logger)

	// Set advertisement-specific data
	dev.name = adv.LocalName()
	dev.rssi = adv.RSSI()
	dev.connectable = adv.Connectable()
	dev.manufData = adv.ManufacturerData()

	// Convert service UUIDs into minimal Service entries (UUID only)
	for _, uuid := range adv.Services() {
		dev.advertisedServices = append(dev.advertisedServices, device.NormalizeUUID(uuid))
	}
	sort.Strings(dev.advertisedServices)

	// Convert service data
	for _, svcData := range adv.ServiceData() {
		dev.serviceData[device.NormalizeUUID(svcData.UUID)] = svcData.Data
	}

	// Extract TX power if available
	if adv.TxPowerLevel() != 127 { // 127 means TX power not available
		txPower := int(adv.TxPowerLevel())
		dev.txPower = &txPower
	}

	// Parse manufacturer data if available
	// Note: BLE advertisements do not provide a separate company/vendor field.
	// The manufacturer data is raw bytes, and the company ID (if present) is
	// embedded in the data itself (conventionally first 2 bytes, little-endian).
	// Therefore, we always use UnknownCompanyID and let the parser extract the
	// company ID from the raw data.
	if len(dev.manufData) > 0 {
		dev.parsedManufData, _ = device.ParseManufacturerData(device.UnknownCompanyID, dev.manufData)
	}

	// Try to extract name from manufacturer data if no local name
	if dev.name == "" {
		if extractedName := dev.extractNameFromManufacturerData(adv.ManufacturerData()); extractedName != "" {
			dev.name = extractedName
		}
	}

	return dev
}

// NewBLEDeviceWithAddress creates a BLEDevice with the specified address
func NewBLEDeviceWithAddress(address string, logger *logrus.Logger) *BLEDevice {
	// Use the new constructor with preconnection
	return NewBLEDevice(address, logger)
}

// Device interface implementation

func (d *BLEDevice) ID() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.id
}

func (d *BLEDevice) Name() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.name == "" {
		return d.address
	}
	return d.name
}

func (d *BLEDevice) Address() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.address
}

func (d *BLEDevice) RSSI() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.rssi
}

func (d *BLEDevice) TxPower() *int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.txPower
}

func (d *BLEDevice) IsConnectable() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.connectable
}

func (d *BLEDevice) AdvertisedServices() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	return d.advertisedServices
}

func (d *BLEDevice) ManufacturerData() []byte {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.manufData
}

func (d *BLEDevice) ParsedManufacturerData() interface{} {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.parsedManufData
}

func (d *BLEDevice) ServiceData() map[string][]byte {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.serviceData
}

// Connect establishes a BLE connection and populates live characteristics
func (d *BLEDevice) Connect(ctx context.Context, opts *device.ConnectOptions) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Defensive check: connection should never be nil with the preconnection pattern
	if d.connection == nil {
		return fmt.Errorf("connect: %w", device.ErrNotInitialized)
	}

	// Set default options if not provided
	if opts == nil {
		opts = &device.ConnectOptions{
			ConnectTimeout: 30 * time.Second,
		}
	}

	// Use the pre-created BLEConnection to connect
	if err := d.connection.Connect(ctx, d.address, opts); err != nil {
		return err
	}

	// Try to resolve device name from GAP Device Name characteristic (0x2A00)
	// GAP Device Name is more authoritative than the advertisement name
	const (
		gapServiceUUID = "1800"
		deviceNameChar = "2a00"
	)

	// Check if GAP service exists
	if _, exists := d.connection.services[gapServiceUUID]; exists {
		// Get the Device Name characteristic

		if char, err := d.connection.GetCharacteristic(gapServiceUUID, deviceNameChar); err == nil {
			// Type-assert to *BLECharacteristic to access BLEChar field
			if bleChar, ok := char.(*BLECharacteristic); ok && bleChar.BLEChar != nil {
				// Read the characteristic value
				if data, err := d.connection.client.ReadCharacteristic(bleChar.BLEChar); NormalizeError(err) == nil && len(data) > 0 {
					name := string(data)
					name = strings.TrimRight(name, "\x00")
					name = strings.TrimSpace(name)

					if len(name) > 0 && isValidDeviceName(name) {
						d.name = name
						d.logger.WithFields(logrus.Fields{
							"address": d.address,
							"name":    name,
						}).Debug("Resolved device name from GAP")
					}
				}
			}
		}
	}

	return nil
}

// Disconnect closes the connection and clears live handles
func (d *BLEDevice) Disconnect() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Defensive check: connection should never be nil with the preconnection pattern
	if d.connection == nil {
		return fmt.Errorf("disconnect: %w", device.ErrNotInitialized)
	}

	// Use the BLEConnection to disconnect
	return d.connection.Disconnect()
}

// isConnectedInternal returns connection status without acquiring locks (for internal use)
func (d *BLEDevice) isConnectedInternal() bool {
	// Defensive check: connection should never be nil with the preconnection pattern
	if d.connection == nil {
		return false
	}

	return d.connection.IsConnected()
}

// IsConnected returns connection status
func (d *BLEDevice) IsConnected() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.isConnectedInternal()
}

// Update refreshes device information from a new advertisement
func (d *BLEDevice) Update(adv device.Advertisement) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.rssi = adv.RSSI()
	d.lastSeen = time.Now()

	// Update name if it wasn't available before or changed
	if name := adv.LocalName(); name != "" {
		d.name = name
	} else if d.name == "" {
		// Try to extract name from manufacturer data if no local name
		if extractedName := d.extractNameFromManufacturerData(adv.ManufacturerData()); extractedName != "" {
			d.name = extractedName
		}
	}

	// Update manufacturer data and parse if changed
	if manufData := adv.ManufacturerData(); len(manufData) > 0 {
		// Only parse if raw manufacturer data changed (cheap byte comparison)
		if !bytes.Equal(d.manufData, manufData) {
			d.manufData = manufData

			// Parse manufacturer data (see NewBLEDeviceFromAdvertisement for explanation of UnknownCompanyID)
			newParsed, _ := device.ParseManufacturerData(device.UnknownCompanyID, d.manufData)

			// Log if parsed data changed (e.g., firmware update, device type change)
			if !reflect.DeepEqual(d.parsedManufData, newParsed) {
				d.logger.WithFields(logrus.Fields{
					"address": d.address,
					"old":     d.parsedManufData,
					"new":     newParsed,
				}).Info("Manufacturer data changed")
			}

			d.parsedManufData = newParsed
		}
	}

	// Merge advertised services (ensure UUID entries exist)
	needsSort := false
	for _, svc := range adv.Services() {
		normalizedSvc := device.NormalizeUUID(svc)
		if !d.hasServiceUUID(normalizedSvc) {
			d.advertisedServices = append(d.advertisedServices, normalizedSvc)
			needsSort = true
		}
	}
	if needsSort {
		sort.Strings(d.advertisedServices)
	}

	// Update service data
	for _, svcData := range adv.ServiceData() {
		d.serviceData[device.NormalizeUUID(svcData.UUID)] = svcData.Data
	}

	// Update TX power
	if adv.TxPowerLevel() != 127 {
		txPower := int(adv.TxPowerLevel())
		d.txPower = &txPower
	}
}

// BLE-specific methods

// WriteToCharacteristic writes data to a BLE characteristic
func (d *BLEDevice) WriteToCharacteristic(uuid string, data []byte) error {
	d.mu.RLock()
	if d.connection == nil {
		d.mu.RUnlock()
		return fmt.Errorf("write to characteristic %s: %w", uuid, device.ErrNotConnected)
	}
	conn := d.connection
	d.mu.RUnlock()

	// Acquire connection lock to check state and snapshot client/characteristics atomically
	conn.connMutex.RLock()
	if !conn.isConnectedInternal() {
		conn.connMutex.RUnlock()
		return device.ErrNotConnected
	}

	// Find characteristic across all services
	var char *BLECharacteristic
	var serviceUUID string
	for svcUUID, service := range conn.services {
		if bleChar, ok := service.Characteristics[uuid]; ok {
			char = bleChar
			serviceUUID = svcUUID
			break
		}
	}

	if char == nil {
		conn.connMutex.RUnlock()
		return &device.NotFoundError{Resource: "characteristic", UUIDs: []string{uuid}}
	}

	if char.BLEChar == nil {
		conn.connMutex.RUnlock()
		return fmt.Errorf("characteristic %s: %w", uuid, device.ErrNotConnected)
	}

	// Snapshot client reference before releasing read lock
	client := conn.client
	conn.connMutex.RUnlock()

	// Acquire write mutex to serialize writes
	conn.writeMutex.Lock()
	defer conn.writeMutex.Unlock()

	// Write data in chunks
	for len(data) > 0 {
		n := len(data)
		if n > DefaultBLEWriteChunkSize {
			n = DefaultBLEWriteChunkSize
		}
		if err := client.WriteCharacteristic(char.BLEChar, data[:n], false); err != nil {
			return fmt.Errorf("failed to write to characteristic %s in service %s: %w", uuid, serviceUUID, NormalizeError(err))
		}
		data = data[n:]
		time.Sleep(DefaultBLEWriteDelay)
	}
	return nil
}

// GetBLEServices returns services with their characteristics
func (d *BLEDevice) GetBLEServices() ([]*BLEService, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Return connected services if device is connected
	if d.isConnectedInternal() {
		result := make([]*BLEService, 0, len(d.connection.services))
		for _, svc := range d.connection.services {
			result = append(result, svc)
		}
		return result, nil
	}

	// Return error if not connected
	return nil, device.ErrNotConnected
}

// GetCharacteristics returns all characteristics as device.Characteristic
func (d *BLEDevice) GetCharacteristics() ([]device.Characteristic, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Return connected characteristics if device is connected
	if d.isConnectedInternal() {
		var result []device.Characteristic
		for _, service := range d.connection.services {
			for _, char := range service.Characteristics {
				result = append(result, char)
			}
		}
		return result, nil
	}

	// Return error if is not connected
	return nil, device.ErrNotConnected
}

// SetDataHandler sets callback for received notifications
func (d *BLEDevice) SetDataHandler(f func(uuid string, data []byte)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onData = f
}

// GetConnection returns the BLE connection interface
func (d *BLEDevice) GetConnection() device.Connection {
	return d.connection
}

// Helper functions

// extractNameFromManufacturerData attempts to extract a device name from manufacturer data
func (d *BLEDevice) extractNameFromManufacturerData(data []byte) string {
	if len(data) < 4 {
		return ""
	}

	// Common patterns in manufacturer data that may contain device names:

	// Pattern 1: Look for readable ASCII strings longer than 3 characters
	// Many devices embed their name as ASCII text in manufacturer data
	for i := 0; i < len(data)-3; i++ {
		if isReadableASCII(data[i]) {
			// Found start of potential string, extract it
			var nameBytes []byte
			for j := i; j < len(data) && j < i+32; j++ { // Limit to 32 chars
				if isReadableASCII(data[j]) {
					nameBytes = append(nameBytes, data[j])
				} else {
					break
				}
			}

			if len(nameBytes) >= 3 { // Minimum meaningful name length
				name := strings.TrimSpace(string(nameBytes))
				if len(name) >= 3 && isValidDeviceName(name) {
					return name
				}
			}
		}
	}

	// Pattern 2: Manufacturer-specific data parsing
	// Note: Vendor-specific manufacturer data parsing is complex and varies by device.
	// For now, we don't parse manufacturer data - it's kept for future implementation.
	// Manufacturer IDs reference: Apple=0x004C, Microsoft=0x0006, Broadcom=0x000F
	_ = data // Suppress unused parameter warning

	return ""
}

// isReadableASCII checks if a byte represents a readable ASCII character
func isReadableASCII(b byte) bool {
	return b >= 32 && b <= 126 && unicode.IsPrint(rune(b))
}

// isValidDeviceName checks if a string looks like a valid device name
func isValidDeviceName(name string) bool {
	if len(name) < 3 || len(name) > 32 {
		return false
	}

	// Must contain at least one letter
	hasLetter := false
	for _, r := range name {
		if unicode.IsLetter(r) {
			hasLetter = true
			break
		}
	}

	return hasLetter
}

// hasServiceUUID checks if services already contain a service with the given UUID (case-insensitive)
func (d *BLEDevice) hasServiceUUID(uuid string) bool {
	// First check connected services if a device is connected
	if d.isConnectedInternal() {
		for _, service := range d.connection.services {
			if strings.EqualFold(service.UUID(), uuid) {
				return true
			}
		}
	}

	// Fall back to advertised services
	for _, s := range d.advertisedServices {
		if strings.EqualFold(s, uuid) {
			return true
		}
	}
	return false
}
