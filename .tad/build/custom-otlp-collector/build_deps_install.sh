#!/bin/bash
# deps_install.sh - Dependency installation script
# This script is used to install build-time dependencies in a separate Docker layer
# to leverage Docker's layer caching mechanism for faster builds.
#
# IMPORTANT: This script is executed BEFORE build.sh in the Dockerfile
#
# Environment Variables (automatically set by Dockerfile):
#   - BUILD_OUTPUT_DIR: Absolute path to build output directory (e.g., /opt/dist)
#   - PROJECT_ROOT: Absolute path to project root (e.g., /opt)
#
# Usage Scenarios:
#   1. Install language-specific dependencies (npm install, pip install, go mod download, etc.)
#   2. Download external tools or binaries
#   3. Set up build environment
#
# DO NOT:
#   - Perform actual compilation/build (that's build.sh's job)
#   - Copy/move source files (Dockerfile handles this)
#
# ============================================
# Script Setup
# ============================================

set -e # Exit on error

cd "${PROJECT_ROOT}"

echo "========================================="
echo "TCS Dependency Installation"
echo "Project root: ${PROJECT_ROOT}"
echo "Build output: ${BUILD_OUTPUT_DIR}"
echo "========================================="

# ============================================
# 1. Install System Packages
# ============================================
echo "No system packages to install"
echo ""

# ============================================
# 2. Ensure Build Output Directory Exists
# ============================================
echo "Ensuring build output directory exists..."
mkdir -p "${BUILD_OUTPUT_DIR}"
echo "✓ Build output directory ready: ${BUILD_OUTPUT_DIR}"
echo ""

# ============================================
# 3. Install Custom Packages
# ============================================
echo "No custom packages to install"
echo ""


# ============================================
# 4. Install Language Dependencies
# ============================================
echo "Installing go dependencies..."
go env -w GOPROXY="https://goproxy.woa.com,direct"
go env -w GOSUMDB="sum.woa.com+643d7a06+Ac5f5VOC4N8NUXdmhbm8pZSXIWfhek5JSmWdWrq7pLX4"

# Execute language-specific dependency installation command
# This command is automatically configured based on:
# 1. Language type (go, python, nodejs, java, rust, etc.)
# 2. Custom deps_install_command from config (if specified)
# 3. Variable substitution (${BUILD_OUTPUT_DIR}, ${PROJECT_ROOT}, etc.)
go mod download

echo "✓ go dependencies installed successfully"

echo "=========================================="
echo "Build Dependencies Installation Completed"
echo "=========================================="
