package mcpserver

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/happyarch/stigmergy/internal/serr"
)

// toolError renders an error as the JSON body the protocol promises:
//
//	{"error":{"code":"cas_conflict","message":"…","current":{…}}}
//
// The go-sdk packs a returned error's message into the tool result as text with
// isError set, so making the message *be* the JSON body gives agents a stable,
// parseable shape instead of prose they have to guess at. Codes are the closed
// set in package serr; anything unrecognized becomes "internal", so an
// unexpected failure can never leak an unstable code or a raw driver message.
func toolError(err error) error {
	if err == nil {
		return nil
	}
	e, ok := serr.As(err)
	if !ok {
		e = serr.E(serr.Internal, "%s", err.Error())
	}

	body := map[string]any{"code": string(e.Code), "message": e.Message}
	for k, v := range e.Context {
		if k != "code" && k != "message" {
			body[k] = v
		}
	}
	payload, jsonErr := json.Marshal(map[string]any{"error": body})
	if jsonErr != nil {
		return fmt.Errorf(`{"error":{"code":"internal","message":%q}}`, e.Message)
	}
	return errors.New(string(payload))
}
