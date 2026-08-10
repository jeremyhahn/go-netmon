# Cisco MIBs

This directory should contain Cisco MIB files needed for monitoring Cisco devices.

## Required MIBs for Cisco Router Monitoring

### Core System MIBs
- **CISCO-PROCESS-MIB** - CPU utilization monitoring
- **CISCO-MEMORY-POOL-MIB** - Memory usage monitoring
- **CISCO-ENVMON-MIB** - Environmental monitoring (temperature, fan status, power supply)

### Routing Protocol MIBs
- **BGP4-MIB** - BGP routing protocol monitoring
- **OSPF-MIB** - OSPF routing protocol monitoring
- **EIGRP-MIB** - EIGRP routing protocol monitoring

### Interface & Performance MIBs
- **OLD-CISCO-INTERFACES-MIB** - Legacy interface statistics
- **CISCO-IF-EXTENSION-MIB** - Extended interface statistics
- **CISCO-RTTMON-MIB** - Round-trip time monitoring

## How to Obtain Cisco MIBs

### Option 1: Download from Cisco Website (Recommended)
1. Go to https://software.cisco.com/download/home
2. Navigate to your device model
3. Download the "SNMP MIBs" package for your IOS version
4. Extract the .my files to this directory

### Option 2: From Cisco FTP Server
```bash
# Note: This requires Cisco CCO credentials
ftp ftp.cisco.com
cd pub/mibs/v2
mget CISCO-*.my
```

### Option 3: Using snmp-mibs-downloader (Linux)
```bash
# Install downloader
sudo apt-get install snmp-mibs-downloader

# MIBs will be in /usr/share/snmp/mibs
# Copy Cisco MIBs to this directory:
cp /usr/share/snmp/mibs/ietf/CISCO-* .
```

### Option 4: From Device Directly
If you have SNMP access to a Cisco device:
```bash
# Some Cisco devices can serve their MIBs via TFTP
# Check your device documentation
```

## File Naming Convention

Cisco MIB files typically use the `.my` extension:
- CISCO-PROCESS-MIB.my
- CISCO-MEMORY-POOL-MIB.my
- CISCO-ENVMON-MIB.my

The parser can handle both `.my` and `.txt` extensions.

## Dependencies

Cisco MIBs often depend on these standard MIBs (already in ../standard/):
- SNMPv2-SMI
- SNMPv2-TC
- SNMPv2-CONF
- IF-MIB
- IANAifType-MIB

## Testing

Once you've added Cisco MIBs, test them with:

```bash
cd /home/user/sources/go-netmon
go run cmd/mib-loader/main.go cisco CISCO-PROCESS-MIB
```

## For Production Use

Ensure you comply with Cisco's licensing terms when using their MIBs in production environments.
