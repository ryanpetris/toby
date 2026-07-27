// Package hostaction defines the JSON-RPC 2.0 envelope and method dispatcher
// used for reverse requests from agent resources to a live launch.
//
// The envelope and Router are used in-process by a launch-owned reverse
// capability: agent resources send host Git requests back to the live
// CLI, which dispatches them through the Router.
package hostaction
