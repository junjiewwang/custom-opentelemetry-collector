#!/bin/bash
# build.sh - Service build script
# This script is used to build the service and prepare deployment artifacts.
#
# Environment Variables (automatically set by Dockerfile):
#   - BUILD_OUTPUT_DIR: Absolute path to build output directory (e.g., /opt/dist)
#   - PROJECT_ROOT: Absolute path to project root (e.g., /opt)
#
# For local builds, these variables will be calculated automatically if not set.

set -e # Exit on error

SERVICE_NAME=custom-otlp-collector

# ============================================
# Build Output Directory Configuration
# ============================================
#
# In Docker build: BUILD_OUTPUT_DIR=/opt/dist, PROJECT_ROOT=/opt (set by Dockerfile)
#
# DO NOT hardcode "dist" anywhere in your scripts - always use ${BUILD_OUTPUT_DIR}

# Check required environment variables
if [ -z "${PROJECT_ROOT}" ]; then
	echo "ERROR: PROJECT_ROOT environment variable is not set!"
	echo ""
	echo "This script must be run in a Docker container where PROJECT_ROOT is set by Dockerfile."
	echo "Expected: PROJECT_ROOT=/opt (or your configured project root)"
	echo ""
	echo "If you're seeing this error:"
	echo "  1. Make sure you're building with Docker (not running the script directly)"
	echo "  2. Check that your Dockerfile sets: ENV PROJECT_ROOT=/opt"
	echo "  3. Verify the Dockerfile is using the correct base image"
	exit 1
fi

# Change to project root directory
cd "${PROJECT_ROOT}"

# If BUILD_OUTPUT_DIR not set, use PROJECT_ROOT/dist as default
BUILD_OUTPUT_DIR="${BUILD_OUTPUT_DIR:-${PROJECT_ROOT}/dist}"

# Set SERVICE_ROOT (project root, same as PROJECT_ROOT in container context)
# In container: PROJECT_ROOT=/opt, SERVICE_ROOT=/opt
# This allows build commands to reference ${SERVICE_ROOT}
SERVICE_ROOT="${PROJECT_ROOT}"

export BUILD_OUTPUT_DIR
export PROJECT_ROOT
export SERVICE_NAME
export SERVICE_ROOT

echo "Project root: ${PROJECT_ROOT}"
echo "Service root: ${SERVICE_ROOT}"
echo "Build output directory: ${BUILD_OUTPUT_DIR}"


# ============================================
# Standardized Multi-language Build Support
# ============================================
echo "========================================="
echo "TCS Service Build System"
echo "Service: ${SERVICE_NAME}"
echo "========================================="

# Create output directories
mkdir -p ${BUILD_OUTPUT_DIR}

# ============================================
# Execute pre-build commands
# ============================================
echo "Executing pre-build commands..."
echo "Pre-build stage"

[ $? -ne 0 ] && echo "ERROR: Pre-build commands failed" && exit 1
echo "Pre-build completed successfully"

# ============================================
# Execute build commands
# ============================================
echo "Executing build commands..."
# Ensure we're in the project root directory before building
cd ${PROJECT_ROOT}
echo "Build stage"
go mod tidy
CGO_ENABLED=0 go build -ldflags="-s -w" -o ${BUILD_OUTPUT_DIR}/bin/${SERVICE_NAME} ./cmd/customcol

[ $? -ne 0 ] && echo "ERROR: Build commands failed" && exit 1
echo "Build completed successfully"

# ============================================
# Execute post-build commands
# ============================================
echo "Executing post-build commands..."
echo "Post-build completed"

[ $? -ne 0 ] && echo "ERROR: Post-build commands failed" && exit 1
echo "Post-build completed successfully"

# ============================================
# Deploy artifacts
# ============================================
target_path=/usr/local/services/${SERVICE_NAME}
# create service dir
mkdir -p ${target_path}

# Copy build artifacts from BUILD_OUTPUT_DIR directory
if [ ! -d "${BUILD_OUTPUT_DIR}" ]; then
	echo "ERROR: ${BUILD_OUTPUT_DIR}/ directory not found!"
	echo ""
	echo "The build commands must output artifacts to the ${BUILD_OUTPUT_DIR}/ directory."
	echo "Please ensure build commands create and populate ${BUILD_OUTPUT_DIR}/"
	exit 1
fi

echo "Copying artifacts from ${BUILD_OUTPUT_DIR}/ to ${target_path}/"
cp -rf ${BUILD_OUTPUT_DIR}/* ${target_path}/

[ $? -ne 0 ] && exit

# ============================================
# Generate runtime scripts
# ============================================
echo "Generating runtime scripts..."

# 复制运行时脚本到服务目录
SERVICE_DIR="/usr/local/services/custom-otlp-collector"
# Use container path (absolute path in container) instead of host relative path
CI_CONTAINER_DIR="/opt/.tad/build/custom-otlp-collector"

mkdir -p ${SERVICE_DIR}

if [ -f "${CI_CONTAINER_DIR}/entrypoint.sh" ]; then
	cp -f ${CI_CONTAINER_DIR}/entrypoint.sh ${SERVICE_DIR}/entrypoint.sh
	chmod +x ${SERVICE_DIR}/entrypoint.sh
	echo "✓ Generated ${SERVICE_DIR}/entrypoint.sh"
fi

if [ -f "${CI_CONTAINER_DIR}/healthchk.sh" ]; then
	cp -f ${CI_CONTAINER_DIR}/healthchk.sh ${SERVICE_DIR}/healthcheck.sh
	chmod +x ${SERVICE_DIR}/healthcheck.sh
	echo "✓ Generated ${SERVICE_DIR}/healthcheck.sh"
fi

echo "Done!"
