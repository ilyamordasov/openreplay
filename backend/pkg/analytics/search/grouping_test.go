package search

import (
	"testing"

	"openreplay/backend/pkg/analytics/model"
)

func strPtr(v string) *string { return &v }

func TestGroupNearbySessionsUsesGapFromPreviousEnd(t *testing.T) {
	sessions := []model.Session{
		{SessionId: "a", UserId: "u1", StartTs: 10 * 60 * 60 * 1000, Duration: 90 * 60 * 1000, EventsCount: 1},
		{SessionId: "b", UserId: "u1", StartTs: 13 * 60 * 60 * 1000, Duration: 20 * 60 * 1000, EventsCount: 2},
		{SessionId: "c", UserId: "u1", StartTs: 15 * 60 * 60 * 1000, Duration: 30 * 60 * 1000, EventsCount: 3},
	}

	groups := groupNearbySessions(sessions, 120, "startTs", "asc")
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if got := len(groups[0].Sessions); got != 3 {
		t.Fatalf("expected 3 sessions in group, got %d", got)
	}
	if groups[0].EventsCount != 6 {
		t.Fatalf("expected summed events count 6, got %d", groups[0].EventsCount)
	}
}

func TestGroupNearbySessionsSeparatesUsers(t *testing.T) {
	sessions := []model.Session{
		{SessionId: "a", UserId: "u1", StartTs: 1000, Duration: 1000},
		{SessionId: "b", UserId: "u2", StartTs: 1500, Duration: 1000},
	}

	groups := groupNearbySessions(sessions, 120, "startTs", "asc")
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups for two users, got %d", len(groups))
	}
}

func TestGroupNearbySessionsDoesNotMergeMissingIdentity(t *testing.T) {
	sessions := []model.Session{
		{SessionId: "a", StartTs: 1000, Duration: 1000},
		{SessionId: "b", StartTs: 1500, Duration: 1000},
	}

	groups := groupNearbySessions(sessions, 120, "startTs", "asc")
	if len(groups) != 2 {
		t.Fatalf("expected anonymous sessions without identity to stay separate, got %d groups", len(groups))
	}
}

func TestGroupNearbySessionsFallsBackToAnonymousId(t *testing.T) {
	sessions := []model.Session{
		{SessionId: "a", UserAnonymousId: strPtr("anon-1"), StartTs: 1000, Duration: 1000},
		{SessionId: "b", UserAnonymousId: strPtr("anon-1"), StartTs: 2000, Duration: 1000},
	}

	groups := groupNearbySessions(sessions, 120, "startTs", "asc")
	if len(groups) != 1 {
		t.Fatalf("expected matching anonymous id to group, got %d groups", len(groups))
	}
}
