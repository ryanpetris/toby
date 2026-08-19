package protocol

// Rejects duplicate fields in resource configuration JSON.

import configfile "petris.dev/toby/internal/config/file"

func rejectDuplicateFields(data []byte) error {
	return configfile.RejectDuplicateFields(data)
}
