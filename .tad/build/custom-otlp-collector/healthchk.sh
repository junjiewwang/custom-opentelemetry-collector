#!/bin/sh

# Export service paths as environment variables
export SERVICE_ROOT="/usr/local/services/custom-otlp-collector"
export SERVICE_BIN_DIR="/usr/local/services/custom-otlp-collector/bin"
export SERVICE_NAME="custom-otlp-collector"

# Default healthcheck: check if service process is running
ps=$(ls -l /proc/*/exe 2>/dev/null | grep "${SERVICE_NAME}" | grep -v grep)

# abnormal
[[ "$ps" == "" ]] && exit 1

# normal
exit 0
