package server

import (
	"encoding/json"

	"sentinel-linux/internal/store"
)

// zoneMetaView is a zone's meta as a JSON object for the v2 payload (lines
// only; nil, so the field is omitted, for polygons).
func zoneMetaView(z store.Zone) any {
	if z.Kind != store.ZoneKindLine {
		return nil
	}
	return json.RawMessage(`{"direction":"` + store.LineDirection(z) + `","severity":"` + store.LineSeverity(z) + `"}`)
}
