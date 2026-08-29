// Package approvalsservice contributes the approvals_wait tool to the Toby
// MCP server. The tool blocks until the user decides a pending approval with
// `toby approvals` and, once approved, returns the executed action's result.
package approvalsservice

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"petris.dev/toby/internal/tobymcp"
)

const toolWait = "approvals_wait"

const waitDescription = "Wait for the user to decide a pending Toby approval " +
	"(the user decides by running the toby approvals command in another " +
	"terminal). When approved, the held action runs on the host and this tool " +
	"returns that action's result. On timeout it returns status \"pending\"; " +
	"call it again with the same approval id to keep waiting."

// Service contributes the approvals_wait tool into the MCP server.
type Service struct{}

var _ tobymcp.Contributor = Service{}

// Tools returns the approvals MCP tools.
func (Service) Tools() []tobymcp.Tool {
	return []tobymcp.Tool{
		{Name: toolWait, Register: func(server *mcp.Server, session *tobymcp.Session) {
			mcp.AddTool(server, &mcp.Tool{Name: toolWait, Description: waitDescription}, handler{session}.wait)
		}},
	}
}

// Resources returns no resources; approval guidance lives in the server
// instructions and the Git tool documentation.
func (Service) Resources() []tobymcp.Resource {
	return nil
}
