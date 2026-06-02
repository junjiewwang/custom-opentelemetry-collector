// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package node

// Role represents a coarse-grained node role.
// Roles are composable — a node declares one or more roles,
// and the effective capability set is the union of all role capabilities.
//
// There is no "all" role by design. A full-capability node simply
// declares multiple roles: [writer, reader, purger, ui].
type Role string

const (
	// RoleWriter indicates a node that writes data to storage.
	RoleWriter Role = "writer"
	// RoleReader indicates a node that reads/queries data.
	RoleReader Role = "reader"
	// RolePurger indicates a node that executes data lifecycle purge.
	RolePurger Role = "purger"
	// RoleUI indicates a node that serves the admin management UI.
	RoleUI Role = "ui"
	// RoleAgent indicates a remote Agent that executes Arthas diagnostic tasks.
	RoleAgent Role = "agent"
)

// AllRoles returns all defined roles for validation purposes.
func AllRoles() []Role {
	return []Role{RoleWriter, RoleReader, RolePurger, RoleUI, RoleAgent}
}

// IsValidRole returns true if the given role is a recognized value.
func IsValidRole(r Role) bool {
	for _, valid := range AllRoles() {
		if r == valid {
			return true
		}
	}
	return false
}

// RoleCapabilities maps each role to its implied capabilities.
// A node's effective capabilities = union of all its roles' capabilities.
var RoleCapabilities = map[Role][]Capability{
	RoleWriter: {CapStorageWrite},
	RoleReader: {CapStorageRead, CapQueryServe},
	RolePurger: {CapStorageRead, CapStorageDelete, CapPurgeExecute, CapPurgePlan},
	RoleUI:     {CapUIServe},
	RoleAgent:  {CapArthasExec},
}

// ExpandRoles takes a list of roles and returns the union of all their capabilities.
func ExpandRoles(roles []Role) *CapabilitySet {
	caps := NewCapabilitySet()
	for _, role := range roles {
		if roleCaps, ok := RoleCapabilities[role]; ok {
			caps.Add(roleCaps...)
		}
	}
	return caps
}
