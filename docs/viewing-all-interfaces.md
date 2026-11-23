# How to View All Interfaces in Grafana Dashboards

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

## Current Dashboard Behavior

Each timeseries panel in the IF-MIB dashboard shows **ALL** interfaces as separate lines on a single graph.

### What You Should See

When you open the IF-MIB dashboard:

1. **Select a device** from the `device` dropdown at the top (e.g., "core-01")

2. **Each panel shows multiple colored lines** - one for each interface:
   - Router with 24 interfaces = 24 lines on each graph
   - Each line is a different color
   - Hover over a line to see its value

3. **Legend shows the device, interface name and index** at the bottom of each
   panel (`{{device}} - {{ifname}} [{{ifindex}}]`):
   ```
   core-01 - Gi0/0 [10]
   core-01 - Gi0/1 [11]
   ... (24 total)
   ```
   `ifname` is empty when the device reports none, leaving only the index.

4. **The `ifindex` label is the interface index.** Each sample also carries an
   `oid` label with the full instance OID, whose last number is the same index
   (`.1.3.6.1.2.1.2.2.1.10.10` → interface index 10).

## Troubleshooting: "I Only See One Interface"

If you're only seeing one line on the graphs:

### Check 1: Time Range
- Make sure you have a time range with data (last 1 hour, last 6 hours, etc.)
- Click the time picker in the top-right corner
- Try "Last 1 hour" or "Last 6 hours"

### Check 2: Device Variable
- Check the `device` dropdown at the top
- The `device` variable is multi-select and includes "All"; make sure the device you expect is selected
- Try selecting "core-01" specifically

### Check 3: Legend Visibility
- Scroll down in the panel to see the full legend
- Click on a legend entry to highlight that line
- Click the legend icon to toggle legend visibility

### Check 4: Refresh the Dashboard
- Click the refresh icon in the top-right
- Or press Ctrl+R to reload the page

## Understanding Interface Indices

Interfaces are identified by the `ifindex` label, and by the `ifname` label
where the device reports one.

### Mapping Index to Interface Name

Every interface series already carries `ifindex` and, where the device reports
it, `ifname`, so the legend shows both. To list the mapping for a device, run
this PromQL query in Grafana Explore:
```promql
count by (ifindex, ifname) (snmp_ifInOctets{device="core-01"})
```

## Viewing Specific Interfaces

To focus on specific interfaces, you can:

1. **Click legend entries** to hide/show specific interfaces
2. **Create a new variable** for interface index
3. **Filter queries** by adding `ifindex="10"`, or `ifname="GigabitEthernet0/0/0"`

## Example Queries

### All interfaces for a device:
```promql
snmp_ifInOctets{device="core-01"}
```

### Specific interface (index 1):
```promql
snmp_ifInOctets{device="core-01",ifindex="1"}
```

### Calculate total traffic across all interfaces:
```promql
sum(rate(snmp_ifInOctets{device="core-01"}[5m]))
```

One metric name covers every row of a column, so the row is chosen with label
matchers rather than a `__name__` regex. See [metric naming](metric-naming.md).
