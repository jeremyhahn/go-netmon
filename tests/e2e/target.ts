// The one place the e2e suite's default target is set (#111).
//
// DISPOSABLE_URL is the web UI of the disposable e2e stack: the "e2e" profile
// of docker-compose.test.yml, brought up with "make e2e-up" on the machine that
// runs the browser. That stack polls a simulated SNMP agent (snmpsim serving
// test/integration/snmp-data) into its own Prometheus, so the rendering specs
// have real series to draw, and everything on it is safe to mutate and throw
// away.
//
// Production (monitor-01) is never a default, and its address appears nowhere in
// this suite: it is read-only to the factory (#70). Reaching any other stack is
// a decision made on the command line with NETMON_URL, never one checked in
// here or in a spec:
// scripts/e2e-target-guard.sh fails the build when a spec names an address
// that is not this one.
export const DISPOSABLE_URL = 'http://localhost:8083';

export const BASE = process.env.NETMON_URL || DISPOSABLE_URL;
