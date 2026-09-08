// ABOUTME: Verifies owner-only closure input and aggregate result reads.
// ABOUTME: It ensures result views contain no individual ballot payloads.
package store

import (
	"context"
	"errors"
	"testing"
)

func TestPollForCloseAndResultForOwnerRemainOwnerScoped(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SyncModerators(ctx, []ConfiguredModerator{{ID: "owner", NormalizedEmail: "owner@example.test"}, {ID: "other", NormalizedEmail: "other@example.test"}}); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		"INSERT INTO polls(id,owner_id,question,deadline,places,state,counting_status,version) VALUES ('poll','owner','Question',500,1,'paused','pending',2)",
		"INSERT INTO options(poll_id,id,label,display_order) VALUES ('poll','a','Alice',1),('poll','b','Bob',2)",
		"INSERT INTO participants(id,poll_id) VALUES ('person','poll')",
		`INSERT INTO ballots(poll_id,participant_id,version,preferences_json,accepted_at) VALUES ('poll','person',1,'["a"]',90)`,
	}
	for _, statement := range statements {
		if _, err := st.DB.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	work, err := st.PollForClose(ctx, "owner", "poll")
	if err != nil || len(work.Ballots) != 1 || work.Ballots[0].ParticipantID != "person" {
		t.Fatalf("work=%#v error=%v", work, err)
	}
	if _, err := st.PollForClose(ctx, "other", "poll"); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-owner close read error=%v", err)
	}
	result, err := st.ResultForOwner(ctx, "owner", "poll")
	if err != nil || result.Turnout != 1 || len(result.ResultJSON) != 0 {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if _, err := st.ResultForOwner(ctx, "other", "poll"); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-owner result read error=%v", err)
	}
}
