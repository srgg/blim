package goble

import (
	"sort"

	"github.com/srgg/blim/internal/device"
)

// ----------------------------
// BLE Service
// ----------------------------

// BLEService represents a GATT service and its characteristics
type BLEService struct {
	uuid            string
	knownName       string
	Characteristics map[string]*BLECharacteristic
}

func (s *BLEService) UUID() string {
	return s.uuid
}

func (s *BLEService) KnownName() string {
	return s.knownName
}

func (s *BLEService) GetCharacteristics() []device.Characteristic {
	result := make([]device.Characteristic, 0, len(s.Characteristics))
	for _, char := range s.Characteristics {
		result = append(result, char)
	}
	// Sort by UUID for consistent ordering
	sort.Slice(result, func(i, j int) bool {
		return result[i].UUID() < result[j].UUID()
	})
	return result
}
