// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package node

// InferredComponents describes what components are active in the current process.
// Used by InferRoles to automatically determine node roles.
type InferredComponents struct {
	// HasStorageProvider is true if a storage provider (ES/PG/Hybrid) is active.
	HasStorageProvider bool
	// HasPurger is true if the purger implements IndexLister + SingleIndexPurger.
	HasPurger bool
	// HasAdminExt is true if the admin extension is loaded.
	HasAdminExt bool
	// HasControlPlaneExt is true if the controlplane extension is loaded.
	HasControlPlaneExt bool
}

// InferRoles determines node roles based on what components are actually loaded.
// This implements "convention over configuration" — zero config = correct behavior.
//
// If configuredRoles is non-empty, it takes precedence (explicit override).
// Otherwise, roles are inferred from loaded components.
func InferRoles(components InferredComponents, configuredRoles []Role) []Role {
	// Explicit configuration takes precedence
	if len(configuredRoles) > 0 {
		return configuredRoles
	}

	// Auto-infer from loaded components
	var roles []Role

	if components.HasStorageProvider {
		roles = append(roles, RoleWriter, RoleReader)
	}
	if components.HasPurger {
		roles = append(roles, RolePurger)
	}
	if components.HasAdminExt {
		roles = append(roles, RoleUI)
	}

	// If nothing was inferred (edge case), default to writer+reader
	// to avoid a node with zero capabilities
	if len(roles) == 0 && components.HasStorageProvider {
		roles = []Role{RoleWriter, RoleReader}
	}

	return roles
}

// BuildDescriptor is a convenience function that infers roles, resolves capabilities,
// and constructs the final NodeDescriptor.
//
// configuredRoles: explicit roles from config (may be nil).
// configuredCaps: explicit capabilities from config (may be nil — overrides role derivation).
func BuildDescriptor(
	nodeID string,
	components InferredComponents,
	configuredRoles []Role,
	configuredCaps []Capability,
) *NodeDescriptor {
	roles := InferRoles(components, configuredRoles)

	// If explicit capabilities are configured, use them directly
	if len(configuredCaps) > 0 {
		caps := NewCapabilitySet(configuredCaps...)
		return NewNodeDescriptorWithCaps(nodeID, roles, caps)
	}

	// Otherwise derive from roles
	return NewNodeDescriptor(nodeID, roles)
}
