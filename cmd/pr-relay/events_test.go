package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseEvents(t *testing.T) {
	ok := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"empty", []string{}, nil},
		{"all", []string{"all"}, nil},
		{"ALL upper", []string{"ALL"}, nil},
		{"ci alias", []string{"ci"}, []string{"ci.failed", "ci.settled"}},
		{"comma", []string{"review,comment"}, []string{"review", "comment"}},
		{"case dedupe", []string{"Review", "review"}, []string{"review"}},
		{"alias overlap", []string{"ci", "ci.failed"}, []string{"ci.failed", "ci.settled"}},
		{"order", []string{"comment", "closed,commits"}, []string{"closed", "commits", "comment"}},
		{"trim", []string{" review , conflict "}, []string{"conflict", "review"}},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseEvents(tc.in)
			if err != nil {
				t.Fatalf("parseEvents(%q) error: %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseEvents(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	const valid = "valid: closed, conflict, commits, ci.failed, ci.settled, review, comment, ci, all"
	bad := []struct {
		name string
		in   []string
		want []string
	}{
		{"checkin", []string{"checkin"}, []string{`unknown event type "checkin"`, valid}},
		{"expired", []string{"review,expired"}, []string{`unknown event type "expired"`, valid}},
		{"bogus", []string{"bogus"}, []string{`unknown event type "bogus"`, valid}},
		{"all combined", []string{"all", "review"}, []string{"all cannot be combined with other event types"}},
		{"all combined comma", []string{"ci,all"}, []string{"all cannot be combined with other event types"}},
		{"empty string", []string{""}, []string{"empty event type"}},
		{"blank commas", []string{" , "}, []string{"empty event type"}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseEvents(tc.in)
			if err == nil {
				t.Fatalf("parseEvents(%q) = %q, want error", tc.in, got)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

func TestEventsLabel(t *testing.T) {
	if got := eventsLabel(nil); got != "all" {
		t.Errorf("eventsLabel(nil) = %q, want all", got)
	}
	if got := eventsLabel([]string{"ci.failed", "review"}); got != "ci.failed,review" {
		t.Errorf("eventsLabel = %q", got)
	}
}
