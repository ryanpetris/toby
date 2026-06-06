// Package control defines the JSON-RPC 2.0 message envelope (request/response/error
// types, error codes, build/parse helpers) and the method-dispatch Router that
// routes a decoded request to a registered Capability. The method-specific
// contracts live in control/methods/*.
//
// The envelope and Router are used in-process: the host-side Git tools encode a
// request and dispatch it through the Router (control/host). This package also
// defines the host-identity sentinels (HostUser/HostGroup) the runtime uses when
// provisioning files.
package control
