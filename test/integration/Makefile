# Integration test targets.
#
# Every target runs the suite inside the Docker stack that run-tests.sh brings
# up; nothing here executes on the host. run-tests.sh owns the compose file, the
# compose project name and the teardown, so the stack is defined in exactly one
# place and a single test cannot be run against a different environment from the
# one the full suite uses.
#
# The tests these targets name are the tests that exist. The previous
# test-parser, test-poller and test-webui targets selected TestMIBParser,
# TestPollerToPrometheus and TestWebUIQueries, none of which are in this
# package, and none passed -tags=integration. Both faults are silent: go test
# reports success for a -run pattern that matches nothing, and without the tag
# the only file that needs the stack is not compiled in. Three targets therefore
# reported success while running no tests at all.

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

# The #69 ordering disorder, driven deliberately (#75). Four tests: what the
# real receiver does with disorder inside one request and across two, and what
# the exporter does when two overlapping polls of one device produce each.
test-ordering:
	@$(RUN_TESTS) 'TestDisorderInsideOneRequestIsDiscardedWithoutASound|TestOneReadingBehindTheHeadIsRefusedAtItsMeasuredCost|TestOverlappingPollsAreRepairedAndNothingIsLost|TestAPollTheEndpointHasMovedPastIsDroppedNotRetriedForever'

# Remove any test stack left behind. run-tests.sh cleans up on every exit path;
# this target is for a run killed hard enough to skip the trap.
#
# The logic lives in clean.sh because it is safety-critical and needs tests:
# selecting projects by "holds a container of ours" alone would select the
# DEPLOYMENT's project, since make integration-up can place test services in
# it. See the script's header.
clean:
	@./clean.sh
