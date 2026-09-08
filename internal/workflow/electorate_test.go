// ABOUTME: Verifies grouped electorate parsing and address entitlement boundaries.
// ABOUTME: It protects one-participant-many-address semantics without identity inference.
package workflow

import (
	"reflect"
	"testing"
)

func TestParseElectorateGroupsAddressesWithoutMergingPeople(t *testing.T) {
	participants, err := ParseElectorate([]string{
		"p@example.test, p.alt@example.test, P@EXAMPLE.TEST",
		"q@example.test:q.alt@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Participant{
		{Addresses: []Address{{Delivery: "p@example.test", Normalized: "p@example.test"}, {Delivery: "p.alt@example.test", Normalized: "p.alt@example.test"}}},
		{Addresses: []Address{{Delivery: "q@example.test", Normalized: "q@example.test"}, {Delivery: "q.alt@example.test", Normalized: "q.alt@example.test"}}},
	}
	if !reflect.DeepEqual(participants, want) {
		t.Fatalf("participants = %#v, want %#v", participants, want)
	}
}

func TestParseElectorateRejectsAddressSharedAcrossParticipants(t *testing.T) {
	if _, err := ParseElectorate([]string{"first@example.test", "FIRST@example.test"}); err == nil {
		t.Fatal("expected ambiguous cross-participant address to fail")
	}
}

func TestParseElectorateRejectsDisplayNamesAndEmptyGroups(t *testing.T) {
	for _, rows := range [][]string{{"Person <person@example.test>"}, {"  "}, {"one@example.test,,two@example.test"}} {
		if _, err := ParseElectorate(rows); err == nil {
			t.Fatalf("ParseElectorate(%q) succeeded", rows)
		}
	}
}
