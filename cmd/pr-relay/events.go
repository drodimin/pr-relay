package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/drodimin/pr-relay/internal/model"
)

// Aliases accepted by --events in addition to the model.AllEventTypes names.
const (
	eventsAliasCI  = "ci"
	eventsAliasAll = "all"
)

// parseEvents validates and normalises --events values (SUB-02). Every
// element may hold a comma-separated list. Names are trimmed and lower-cased;
// "ci" expands to ci.failed and ci.settled; "all" (alone) means every type and
// yields nil. Only model.AllEventTypes names are accepted, so the lifecycle
// types can never be selected. The result is deduped and in AllEventTypes
// order. The daemon re-validates the mask.
func parseEvents(in []string) ([]string, error) {
	known := map[string]bool{}
	for _, t := range model.AllEventTypes() {
		known[string(t)] = true
	}
	selected := map[string]bool{}
	sawAll, sawOther := false, false
	for _, elem := range in {
		for _, raw := range strings.Split(elem, ",") {
			name := strings.ToLower(strings.TrimSpace(raw))
			switch {
			case name == "":
				return nil, errors.New("empty event type in --events")
			case name == eventsAliasAll:
				sawAll = true
			case name == eventsAliasCI:
				sawOther = true
				selected[string(model.EventCIFailed)] = true
				selected[string(model.EventCISettled)] = true
			case known[name]:
				sawOther = true
				selected[name] = true
			default:
				return nil, fmt.Errorf("unknown event type %q (%s)", name, validEventsList())
			}
		}
	}
	if sawAll && sawOther {
		return nil, errors.New("all cannot be combined with other event types")
	}
	if len(selected) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(selected))
	for _, t := range model.AllEventTypes() {
		if selected[string(t)] {
			out = append(out, string(t))
		}
	}
	return out, nil
}

// validEventsList is the "valid: ..." hint shown for unknown names.
func validEventsList() string {
	names := make([]string, 0, len(model.AllEventTypes())+2)
	for _, t := range model.AllEventTypes() {
		names = append(names, string(t))
	}
	names = append(names, eventsAliasCI, eventsAliasAll)
	return "valid: " + strings.Join(names, ", ")
}

// eventsLabel renders a normalised mask: "all" when empty, else comma-joined.
func eventsLabel(ev []string) string {
	if len(ev) == 0 {
		return eventsAliasAll
	}
	return strings.Join(ev, ",")
}
