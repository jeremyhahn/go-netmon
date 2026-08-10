package snmp

import (
	"fmt"
	"math"
	"time"

	"github.com/gosnmp/gosnmp"
)

// Result represents an SNMP query result
type Result struct {
	OID       string
	Type      gosnmp.Asn1BER
	Value     interface{}
	Timestamp time.Time
}

// DeviceInfo contains basic SNMP device information
type DeviceInfo struct {
	Address     string
	Name        string
	Description string
	Location    string
	Contact     string
	Uptime      time.Duration
}

// String returns a string representation of the result
func (r *Result) String() string {
	return fmt.Sprintf("OID: %s, Type: %s, Value: %v", r.OID, r.TypeString(), r.Value)
}

// TypeString returns a human-readable type name
func (r *Result) TypeString() string {
	switch r.Type {
	case gosnmp.Boolean:
		return "Boolean"
	case gosnmp.Integer:
		return "Integer"
	case gosnmp.BitString:
		return "BitString"
	case gosnmp.OctetString:
		return "OctetString"
	case gosnmp.Null:
		return "Null"
	case gosnmp.ObjectIdentifier:
		return "ObjectIdentifier"
	case gosnmp.ObjectDescription:
		return "ObjectDescription"
	case gosnmp.IPAddress:
		return "IPAddress"
	case gosnmp.Counter32:
		return "Counter32"
	case gosnmp.Gauge32:
		return "Gauge32"
	case gosnmp.TimeTicks:
		return "TimeTicks"
	case gosnmp.Opaque:
		return "Opaque"
	case gosnmp.NsapAddress:
		return "NsapAddress"
	case gosnmp.Counter64:
		return "Counter64"
	case gosnmp.Uinteger32:
		return "Uinteger32"
	case gosnmp.NoSuchObject:
		return "NoSuchObject"
	case gosnmp.NoSuchInstance:
		return "NoSuchInstance"
	case gosnmp.EndOfMibView:
		return "EndOfMibView"
	default:
		return fmt.Sprintf("Unknown(%d)", r.Type)
	}
}

// IsCounter returns true if the result is a counter type
func (r *Result) IsCounter() bool {
	return r.Type == gosnmp.Counter32 || r.Type == gosnmp.Counter64
}

// IsGauge returns true if the result is a gauge type
func (r *Result) IsGauge() bool {
	return r.Type == gosnmp.Gauge32 || r.Type == gosnmp.Uinteger32 || r.Type == gosnmp.Integer
}

// IsString returns true if the result is a string type
func (r *Result) IsString() bool {
	return r.Type == gosnmp.OctetString
}

// GetInt64Value attempts to convert the value to int64
func (r *Result) GetInt64Value() (int64, error) {
	switch v := r.Value.(type) {
	case int:
		return int64(v), nil
	case int8:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case uint:
		// uint is 64 bits here, so it needs the same guard as uint64 below.
		if v > math.MaxInt64 {
			return 0, fmt.Errorf("value %d exceeds int64 max", v)
		}
		return int64(v), nil
	case uint8:
		return int64(v), nil
	case uint16:
		return int64(v), nil
	case uint32:
		return int64(v), nil
	case uint64:
		if v > math.MaxInt64 {
			return 0, fmt.Errorf("value %d exceeds int64 max", v)
		}
		return int64(v), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to int64", v)
	}
}

// GetUint64Value attempts to convert the value to uint64
func (r *Result) GetUint64Value() (uint64, error) {
	switch v := r.Value.(type) {
	case int:
		if v < 0 {
			return 0, fmt.Errorf("negative value %d cannot be converted to uint64", v)
		}
		return uint64(v), nil
	case int8:
		if v < 0 {
			return 0, fmt.Errorf("negative value %d cannot be converted to uint64", v)
		}
		return uint64(v), nil
	case int16:
		if v < 0 {
			return 0, fmt.Errorf("negative value %d cannot be converted to uint64", v)
		}
		return uint64(v), nil
	case int32:
		if v < 0 {
			return 0, fmt.Errorf("negative value %d cannot be converted to uint64", v)
		}
		return uint64(v), nil
	case int64:
		if v < 0 {
			return 0, fmt.Errorf("negative value %d cannot be converted to uint64", v)
		}
		return uint64(v), nil
	case uint:
		return uint64(v), nil
	case uint8:
		return uint64(v), nil
	case uint16:
		return uint64(v), nil
	case uint32:
		return uint64(v), nil
	case uint64:
		return v, nil
	default:
		return 0, fmt.Errorf("cannot convert %T to uint64", v)
	}
}

// GetStringValue attempts to convert the value to string
func (r *Result) GetStringValue() (string, error) {
	switch v := r.Value.(type) {
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	default:
		return "", fmt.Errorf("cannot convert %T to string", v)
	}
}

// GetFloat64Value attempts to convert the value to float64
func (r *Result) GetFloat64Value() (float64, error) {
	intVal, err := r.GetInt64Value()
	if err != nil {
		return 0, err
	}
	return float64(intVal), nil
}

// String returns a string representation of device info
func (d *DeviceInfo) String() string {
	return fmt.Sprintf("Device: %s (%s) - %s, Uptime: %s",
		d.Name, d.Address, d.Description, d.Uptime)
}
