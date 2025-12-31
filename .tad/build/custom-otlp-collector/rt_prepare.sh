#!/bin/sh
# rt_prepare.sh - Runtime preparation script
# This script is used to install runtime dependencies and prepare the environment
# for the application to run properly.

set -e

# ============================================
# Runtime Dependencies Installation
# ============================================
echo "========================================="
echo "TCS Runtime Preparation"
echo "========================================="

# Detect architecture
ARCH=$(uname -m)
echo "Detected architecture: ${ARCH}"
echo ""

# ============================================
# Install Runtime Dependencies
# ============================================
# Runtime dependencies include both executable tools and data packages
# Examples: tzdata, ca-certificates, curl, wget, etc.
RUNTIME_PACKAGES=""

if [ -n "$RUNTIME_PACKAGES" ]; then
	echo "Installing runtime dependencies: $RUNTIME_PACKAGES"
	echo ""

	# Detect package manager and install all packages at once
	if command -v apk >/dev/null 2>&1; then
		echo "Using apk package manager..."
		apk add --no-cache $RUNTIME_PACKAGES || {
			echo "ERROR: Failed to install packages with apk"
			exit 1
		}
	elif command -v apt-get >/dev/null 2>&1; then
		echo "Using apt-get package manager..."
		apt-get update -qq && apt-get install -y $RUNTIME_PACKAGES || {
			echo "ERROR: Failed to install packages with apt-get"
			exit 1
		}
	elif command -v yum >/dev/null 2>&1; then
		echo "Using yum package manager..."
		yum install -y $RUNTIME_PACKAGES || {
			echo "ERROR: Failed to install packages with yum"
			exit 1
		}
	elif command -v dnf >/dev/null 2>&1; then
		echo "Using dnf package manager..."
		dnf install -y $RUNTIME_PACKAGES || {
			echo "ERROR: Failed to install packages with dnf"
			exit 1
		}
	elif command -v zypper >/dev/null 2>&1; then
		echo "Using zypper package manager..."
		zypper install -y $RUNTIME_PACKAGES || {
			echo "ERROR: Failed to install packages with zypper"
			exit 1
		}
	else
		echo "ERROR: No supported package manager found"
		echo "Supported package managers: apk, apt-get, yum, dnf, zypper"
		exit 1
	fi

	echo "✓ All runtime dependencies installed successfully"
	echo ""
else
	echo "No runtime dependencies specified, skipping installation"
	echo ""
fi

# ============================================
# Architecture-specific Dependencies
# ============================================
case "${ARCH}" in
x86_64 | amd64)
	echo "Installing x86_64/amd64 specific dependencies..."
	# Add x86_64 specific packages here
	;;
aarch64 | arm64)
	echo "Installing aarch64/arm64 specific dependencies..."
	# Add ARM64 specific packages here
	;;
*)
	echo "WARNING: Unknown architecture ${ARCH}, skipping architecture-specific dependencies"
	;;
esac

# ============================================
# Verify Essential Dependencies
# ============================================
echo "Verifying essential dependencies..."

# Example: Check if required commands are available
# command -v python3 >/dev/null 2>&1 || { echo "ERROR: python3 not found"; exit 1; }


echo "Runtime preparation completed successfully"
