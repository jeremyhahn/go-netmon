# How to View All Interfaces in Grafana Dashboards

## Current Dashboard Behavior

Each timeseries panel in the IF-MIB dashboard shows **ALL** interfaces as separate lines on a single graph.

### What You Should See

When you open the IF-MIB dashboard:

1. **Select a device** from the `device` dropdown at the top (e.g., "core-01")

2. **Each panel shows multiple colored lines** - one for each interface:
   - Router with 24 interfaces = 24 lines on each graph
   - Each line is a different color
   - Hover over a line to see its value

3. **Legend shows interface OIDs** at the bottom of each panel:
   ```
   core-01 - .1.3.6.1.2.1.2.2.1.10.1
   core-01 - .1.3.6.1.2.1.2.2.1.10.2
   core-01 - .1.3.6.1.2.1.2.2.1.10.3
   ... (24 total)
   ```

4. **The last number in the OID is the interface index**:
   - `.1.3.6.1.2.1.2.2.1.10.1` → Interface index 1
   - `.1.3.6.1.2.1.2.2.1.10.26` → Interface index 26

## Troubleshooting: "I Only See One Interface"

If you're only seeing one line on the graphs:

### Check 1: Time Range
- Make sure you have a time range with data (last 1 hour, last 6 hours, etc.)
- Click the time picker in the top-right corner
- Try "Last 1 hour" or "Last 6 hours"

### Check 2: Device Variable
- Check the `device` dropdown at the top
- Make sure you've selected a device (not "All")
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

To find which index corresponds to which interface, you need to:

1. Log into your router's management interface
2. Check the SNMP interface table
3. Match index numbers to interface names

OR use this PromQL query in Grafana Explore:
```promql
snmp_ifDescr{device="core-01"}
```

Every interface series already carries `ifindex` and, where the device reports
it, `ifname`.

## Future Improvement: Add Interface Names

To make interfaces easier to identify, we can:

1. **Add interface descriptions as labels** when exporting metrics
2. **Use label_replace() in PromQL** to extract index numbers
3. **Create a mapping table** in Grafana

Would you like me to implement any of these improvements?

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
