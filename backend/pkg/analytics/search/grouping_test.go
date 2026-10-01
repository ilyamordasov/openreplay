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


func TestGroupNearbySessionsHonorsTwoHourBoundary(t *testing.T) {
	const hour = uint64(60 * 60 * 1000)
	sessions := []model.Session{
		{SessionId: "a", UserId: "u1", StartTs: 10 * hour, Duration: uint32(hour)},
		{SessionId: "b", UserId: "u1", StartTs: 13 * hour, Duration: uint32(10 * 60 * 1000)},
		{SessionId: "c", UserId: "u1", StartTs: 15*hour + uint64(10*60*1000) + 1, Duration: uint32(10 * 60 * 1000)},
	}

	groups := groupNearbySessions(sessions, 120, "startTs", "asc")
	if len(groups) != 2 {
		t.Fatalf("expected exact 2h gap to group and >2h gap to split, got %d groups", len(groups))
	}
	if got := len(groups[0].Sessions); got != 2 {
		t.Fatalf("expected first group to contain 2 sessions, got %d", got)
	}
}


func TestGroupPagePaginatesGroupsNotSessions(t *testing.T) {
	groups := make([]model.SessionGroup, 15)
	for i := range groups {
		groups[i] = model.SessionGroup{GroupId: string(rune('a' + i))}
	}

	page, total := groupPage(groups, 2, 10)
	if total != 15 {
		t.Fatalf("expected total 15 groups, got %d", total)
	}
	if len(page) != 5 {
		t.Fatalf("expected 5 groups on second page, got %d", len(page))
	}
}

func TestGroupSessionIDsUsesOnlyVisibleGroupMembers(t *testing.T) {
	groups := []model.SessionGroup{
		{Sessions: []model.Session{{SessionId: "101"}, {SessionId: "102"}}},
		{Sessions: []model.Session{{SessionId: "205"}}},
	}

	ids, err := groupSessionIDs(groups)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 3 || ids[0] != 101 || ids[1] != 102 || ids[2] != 205 {
		t.Fatalf("unexpected ids: %#v", ids)
	}
}

func TestGroupSessionIDsRejectsNonNumericDatabaseID(t *testing.T) {
	groups := []model.SessionGroup{
		{Sessions: []model.Session{{SessionId: "not-a-session-id"}}},
	}

	if _, err := groupSessionIDs(groups); err == nil {
		t.Fatal("expected invalid session id to fail closed")
	}
}

func TestHydrateSessionGroupsPreservesGroupingAndOrder(t *testing.T) {
	groups := []model.SessionGroup{
		{
			GroupId: "g1",
			Sessions: []model.Session{
				{SessionId: "101", UserId: "u1"},
				{SessionId: "102", UserId: "u1"},
			},
		},
	}
	full := []model.Session{
		{SessionId: "102", UserId: "u1", UserBrowser: "Safari"},
		{SessionId: "101", UserId: "u1", UserBrowser: "Chrome"},
	}

	got := hydrateSessionGroups(groups, full)
	if got[0].Sessions[0].SessionId != "101" || got[0].Sessions[0].UserBrowser != "Chrome" {
		t.Fatalf("first session order/hydration changed: %#v", got[0].Sessions[0])
	}
	if got[0].Sessions[1].SessionId != "102" || got[0].Sessions[1].UserBrowser != "Safari" {
		t.Fatalf("second session order/hydration changed: %#v", got[0].Sessions[1])
	}
}
