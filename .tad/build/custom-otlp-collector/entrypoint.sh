#!/bin/sh

echo "========================================="
echo "Service Entrypoint"
echo "Service: custom-otlp-collector"
echo "Deploy Dir: /usr/local/services"
echo "========================================="

# Export service paths as environment variables
export SERVICE_ROOT="/usr/local/services/custom-otlp-collector"
export SERVICE_BIN_DIR="/usr/local/services/custom-otlp-collector/bin"
export SERVICE_NAME="custom-otlp-collector"

echo "Service Root: ${SERVICE_ROOT}"
echo "Service Bin Dir: ${SERVICE_BIN_DIR}"
echo "Service Name: ${SERVICE_NAME}"
# Set environment variables
export GO_ENV=production
export LOG_LEVEL=info

# ============================================
# Language-specific start command (CUSTOMIZE THIS SECTION)
# ============================================
# For Go:
#!/bin/sh
set -e
cd ${SERVICE_ROOT}
exec ./bin/${SERVICE_NAME} --config ./config.yaml


# ============================================
