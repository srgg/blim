//go:build test

package testutils

import (
	"encoding/json"
	"fmt"

	"github.com/srg/blim/internal/device"
)

type DeviceJSONFull struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	Address          string        `json:"address"`
	RSSI             int           `json:"rssi"`
	TxPower          *int          `json:"tx_power,omitempty"`
	Connectable      bool          `json:"connectable"`
	LastSeen         int64         `json:"last_seen"`
	Services         []ServiceJSON `json:"services"`
	ManufacturerData interface{}   `json:"manufacturer_data,omitempty"`
	ServiceData      interface{}   `json:"service_data,omitempty"`
}

type ServiceJSON struct {
	UUID            string               `json:"uuid"`
	Characteristics []CharacteristicJSON `json:"characteristics"`
}

type CharacteristicJSON struct {
	UUID        string           `json:"uuid"`
	Properties  string           `json:"properties"`
	Descriptors []DescriptorJSON `json:"descriptors"`
	Value       string           `json:"value"`
}

type DescriptorJSON struct {
	UUID string `json:"uuid"`
}

// AdvertisementToJSON converts a device.Advertisement to JSON string
func AdvertisementToJSON(adv device.Advertisement) string {
	// Convert manufacturer data from []byte to hex string
	var manufDataStr string
	if adv.ManufacturerData() != nil {
		manufDataStr = bytesToHex(adv.ManufacturerData())
	}

	// Convert service data
	var serviceDataMap map[string]string
	if adv.ServiceData() != nil && len(adv.ServiceData()) > 0 {
		serviceDataMap = make(map[string]string)
		for _, sd := range adv.ServiceData() {
			serviceDataMap[sd.UUID] = bytesToHex(sd.Data)
		}
	}

	// Convert TxPowerLevel to pointer for omitempty
	var txPower *int
	if adv.TxPowerLevel() != 0 {
		val := adv.TxPowerLevel()
		txPower = &val
	}

	jsonStruct := struct {
		Address          string            `json:"address"`
		Name             string            `json:"name"`
		RSSI             int               `json:"rssi"`
		Connectable      bool              `json:"connectable"`
		ManufacturerData string            `json:"manufacturer_data,omitempty"`
		ServiceData      map[string]string `json:"service_data,omitempty"`
		TxPower          *int              `json:"tx_power,omitempty"`
		Services         []string          `json:"services,omitempty"`
	}{
		Address:          adv.Addr(),
		Name:             adv.LocalName(),
		RSSI:             adv.RSSI(),
		Connectable:      adv.Connectable(),
		ManufacturerData: manufDataStr,
		ServiceData:      serviceDataMap,
		TxPower:          txPower,
		Services:         adv.Services(),
	}

	b, err := json.Marshal(jsonStruct)
	if err != nil {
		panic(err)
	}

	return string(b)
}

// bytesToHex converts []byte to lowercase hex string
func bytesToHex(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	hexStr := ""
	for _, b := range data {
		hexStr += string("0123456789abcdef"[b>>4]) + string("0123456789abcdef"[b&0x0f])
	}
	return hexStr
}

// DeviceToJSON converts a device. Device to JSON string
func DeviceToJSON(d device.Device) string {
	// Map Services - advertised services are now just UUIDs (no characteristics until connected)
	var services []ServiceJSON
	for _, serviceUUID := range d.AdvertisedServices() {
		services = append(services, ServiceJSON{
			UUID:            serviceUUID,            // serviceUUID is already a string
			Characteristics: []CharacteristicJSON{}, // Advertised services have no characteristics
		})
	}

	// Keep manufacturer and service data as byte arrays (closer to BLE format)
	// Convert []byte to []int to avoid base64 encoding
	var manufData interface{}
	if d.ManufacturerData() != nil {
		byteData := d.ManufacturerData()
		intData := make([]int, len(byteData))
		for i, b := range byteData {
			intData[i] = int(b)
		}
		manufData = intData
	}

	var serviceData interface{}
	if len(d.ServiceData()) > 0 {
		svcData := make(map[string][]int)
		for k, v := range d.ServiceData() {
			intData := make([]int, len(v))
			for i, b := range v {
				intData[i] = int(b)
			}
			svcData[k] = intData
		}
		serviceData = svcData
	}

	jsonStruct := DeviceJSONFull{
		ID:               d.ID(),
		Name:             d.Name(),
		Address:          d.Address(),
		RSSI:             d.RSSI(),
		TxPower:          d.TxPower(),
		Connectable:      d.IsConnectable(),
		Services:         services,
		ManufacturerData: manufData,
		ServiceData:      serviceData,
	}

	b, err := json.Marshal(jsonStruct)
	if err != nil {
		panic(err)
	}

	return string(b) // convert []byte to string
}

type DeviceRecordJSON struct {
	*device.Record
}

func (r DeviceRecordJSON) MarshalJSON() ([]byte, error) {
	obj := make(map[string]interface{})

	// RecordMeta
	obj["TsUs"] = r.Record.TsUs
	obj["Seq"] = r.Record.Seq
	obj["Flags"] = r.Record.Flags

	// Values
	values := make(map[string][]string)
	for k, v := range r.Record.Values {
		hexSlice := make([]string, len(v))
		for i, by := range v {
			hexSlice[i] = fmt.Sprintf("0x%02x", by)
		}
		values[k] = hexSlice
	}
	obj["Values"] = values

	// BatchValues
	batches := make(map[string][][]string)
	for k, batch := range r.Record.BatchValues {
		hexBatches := make([][]string, len(batch))
		for i, b := range batch {
			hexBatch := make([]string, len(b))
			for j, by := range b {
				hexBatch[j] = fmt.Sprintf("0x%02x", by)
			}
			hexBatches[i] = hexBatch
		}
		batches[k] = hexBatches
	}
	obj["BatchValues"] = batches

	// Meta
	if r.Record.Meta == nil {
		obj["Meta"] = map[string][]*device.RecordMeta{}
	} else {
		obj["Meta"] = r.Record.Meta
	}

	return json.Marshal(obj)
}

func (r *DeviceRecordJSON) UnmarshalJSON(data []byte) error {
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}

	// Initialize embedded Record if nil
	if r.Record == nil {
		r.Record = &device.Record{}
	}

	// ---- RecordMeta ----
	if ts, ok := obj["TsUs"].(float64); ok {
		r.TsUs = int64(ts)
	}
	if seq, ok := obj["Seq"].(float64); ok {
		r.Seq = uint64(seq)
	}
	if flags, ok := obj["Flags"].(float64); ok {
		r.Flags = uint32(flags)
	}

	// ---- Values ----
	if valuesRaw, ok := obj["Values"].(map[string]interface{}); ok {
		r.Values = make(map[string][]byte, len(valuesRaw))
		for k, v := range valuesRaw {
			switch val := v.(type) {
			case string:
				// Plain string value (e.g., "XYZ" from Lua callback)
				r.Values[k] = []byte(val)
			case []interface{}:
				// Hex array (e.g., ["0x58", "0x59", "0x5a"] from YAML test)
				bytes := make([]byte, len(val))
				for i, hv := range val {
					if hs, ok := hv.(string); ok {
						b, err := parseHexByte(hs)
						if err != nil {
							return fmt.Errorf("invalid hex byte in Values[%s]: %v", k, err)
						}
						bytes[i] = b
					}
				}
				r.Values[k] = bytes
			}
		}
	}

	if batchesRaw, ok := obj["BatchValues"].(map[string]interface{}); ok {
		r.BatchValues = make(map[string][][]byte, len(batchesRaw))
		for k, batchVal := range batchesRaw {
			if batchArr, ok := batchVal.([]interface{}); ok {
				batches := make([][]byte, len(batchArr))
				for i, b := range batchArr {
					// Handle raw string format from Lua: ["XYZ", "\u0001\u0002\u0003"]
					if rawStr, ok := b.(string); ok {
						batches[i] = []byte(rawStr)
					} else if hexArr, ok := b.([]interface{}); ok {
						// Handle hex array format from tests: [["0x58", "0x59"], ["0x01", "0x02"]]
						bytes := make([]byte, len(hexArr))
						for j, hv := range hexArr {
							if hs, ok := hv.(string); ok {
								b, err := parseHexByte(hs)
								if err != nil {
									return fmt.Errorf("invalid hex byte in BatchValues[%s][%d]: %v", k, i, err)
								}
								bytes[j] = b
							}
						}
						batches[i] = bytes
					}
				}
				r.BatchValues[k] = batches
			}
		}
	}

	// ---- Meta ----
	if metaRaw, ok := obj["Meta"]; ok {
		b, err := json.Marshal(metaRaw)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &r.Meta); err != nil {
			return err
		}
	}

	return nil
}

// ---- Helper: parse hex string like "0x1a" ----
func parseHexByte(s string) (byte, error) {
	var b byte
	_, err := fmt.Sscanf(s, "0x%02x", &b)
	return b, err
}
