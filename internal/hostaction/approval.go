package hostaction

// The error data paired with CodeApprovalRequired. The code is
// capability-agnostic: any method may hold its request for an out-of-band
// approval and identify the pending record through this shape.

import (
	"encoding/json"
	"fmt"
)

// ApprovalRequiredData identifies the pending approval a held request awaits.
type ApprovalRequiredData struct {
	ApprovalID string `json:"approval_id"`
	Name       string `json:"name"`
	Message    string `json:"message"`
}

// DecodeApprovalRequiredData decodes the error data of a CodeApprovalRequired
// response.
func DecodeApprovalRequiredData(data any) (ApprovalRequiredData, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return ApprovalRequiredData{}, fmt.Errorf(
			"encode approval-required data: %w",
			err,
		)
	}
	var decoded ApprovalRequiredData
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ApprovalRequiredData{}, fmt.Errorf(
			"parse approval-required data: %w",
			err,
		)
	}
	if decoded.ApprovalID == "" {
		return ApprovalRequiredData{}, fmt.Errorf(
			"approval-required data has no approval id",
		)
	}
	return decoded, nil
}
