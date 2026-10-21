package main

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// envelope is the JSON-RPC envelope CPA exchanges with a plugin. It is
// identical in shape to pluginabi.Envelope and is used for every inbound
// method call.
type envelope struct {
	OK     bool             `json:"ok"`
	Result json.RawMessage  `json:"result,omitempty"`
	Error  *pluginabi.Error `json:"error,omitempty"`
}

// hostRPCError carries a failed host callback so callers can inspect the code.
type hostRPCError struct {
	Code    string
	Message string
}

func (e *hostRPCError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// okEnvelope marshals v into a success envelope.
func okEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

// errorEnvelope builds a failure envelope. httpStatus is mandatory: an error
// envelope without one makes CPA downgrade the failure to HTTP 500, which the
// management UI reports as a gateway fault.
func errorEnvelope(code, message string, httpStatus int) []byte {
	raw, errMarshal := pluginabi.NewErrorEnvelope(code, message, httpStatus)
	if errMarshal != nil {
		// Marshalling a plain struct cannot realistically fail; keep a hand
		// written fallback so the host never receives an empty response.
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"failed to encode error envelope","http_status":500}}`)
	}
	return raw
}

// marshalJSON is a small helper for fixed payloads that must exist on the wire.
func marshalJSON(v any) []byte {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return []byte(`{}`)
	}
	return raw
}
