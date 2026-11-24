# Integration test targets; each runs inside the Docker stack run-tests.sh manages.

.PHONY: test test-all test-e2e test-snmp test-prometheus test-pipeline test-index test-ordering clean

RUN_TESTS := ./run-tests.sh

# Run the full integration suite.
test:
	@$(RUN_TESTS)

test-all: test

# End-to-end poll of the SNMP simulator through to Prometheus remote write.
test-e2e:
	@$(RUN_TESTS) TestEndToEndIntegration

# SNMP reachability, direct and through the poller.
test-snmp:
	@$(RUN_TESTS) TestSNMPConnectivity

# Prometheus health and query API.
test-prometheus:
	@$(RUN_TESTS) TestPrometheusConnectivity

# MIB pipeline against the in-process SNMP agent: poll, index, remote write.
test-pipeline:
	@$(RUN_TESTS) TestEndToEndPipeline

# Table index extraction from the loaded MIBs.
test-index:
	@$(RUN_TESTS) TestTableIndexExtraction

# The remote-write ordering disorder, driven deliberately. Four tests: what the
# real receiver does with disorder inside one request and across two, and what
# the exporter does when two overlapping polls of one device produce each.
test-ordering:
	@$(RUN_TESTS) 'TestDisorderInsideOneRequestIsDiscardedWithoutASound|TestOneReadingBehindTheHeadIsRefusedAtItsMeasuredCost|TestOverlappingPollsAreRepairedAndNothingIsLost|TestAPollTheEndpointHasMovedPastIsDroppedNotRetriedForever'

# Remove any leftover test stack after a hard kill; the logic and its safety checks live in clean.sh.
clean:
	@./clean.sh
