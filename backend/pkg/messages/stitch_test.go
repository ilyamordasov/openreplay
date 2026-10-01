package messages

import (
	"encoding/binary"
	"testing"
)

func testIndexedReplay(messages ...Message) []byte {
	data := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	var indexBytes [8]byte
	for i, message := range messages {
		binary.LittleEndian.PutUint64(indexBytes[:], uint64(i+1))
		data = append(data, indexBytes[:]...)
		data = append(data, message.Encode()...)
	}
	return data
}

func replayTimestamps(t *testing.T, data []byte) []uint64 {
	t.Helper()
	decoded, err := DecodeReplayStream(data)
	if err != nil {
		t.Fatalf("decode stitched replay: %v", err)
	}
	var timestamps []uint64
	for _, message := range decoded {
		if timestamp, ok := message.(*Timestamp); ok {
			timestamps = append(timestamps, timestamp.Timestamp)
		}
	}
	return timestamps
}

func TestStitchReplayStreamsCompactsSessionGapIntoSingleTimeline(t *testing.T) {
	first := testIndexedReplay(
		&Timestamp{Timestamp: 1_000},
		&SessionStart{Timestamp: 1_000, UserUUID: "fp-a"},
		&CreateDocument{},
		&Timestamp{Timestamp: 2_000},
		&SessionEnd{Timestamp: 2_000},
	)
	second := testIndexedReplay(
		&Timestamp{Timestamp: 10_000},
		&SessionStart{Timestamp: 10_000, UserUUID: "fp-a"},
		&CreateDocument{},
		&Timestamp{Timestamp: 10_500},
		&SessionEnd{Timestamp: 10_500},
	)

	stitched, plan, err := StitchReplayStreams([][]byte{first, second})
	if err != nil {
		t.Fatalf("stitch: %v", err)
	}
	if len(plan) != 2 {
		t.Fatalf("plan segments = %d, want 2", len(plan))
	}
	if plan[0].TargetStart != 1_000 || plan[0].TargetEnd != 2_000 {
		t.Fatalf("first plan = %#v", plan[0])
	}
	if plan[1].TargetStart != 2_001 || plan[1].TargetEnd != 2_501 {
		t.Fatalf("second plan = %#v", plan[1])
	}

	got := replayTimestamps(t, stitched)
	want := []uint64{1_000, 2_000, 2_001, 2_501}
	if len(got) != len(want) {
		t.Fatalf("timestamps = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("timestamps = %#v, want %#v", got, want)
		}
	}
}

func TestStitchReplayStreamsReindexesAcrossSourceSessions(t *testing.T) {
	first := testIndexedReplay(
		&Timestamp{Timestamp: 1_000},
		&CreateDocument{},
		&Timestamp{Timestamp: 1_100},
	)
	second := testIndexedReplay(
		&Timestamp{Timestamp: 2_000},
		&CreateDocument{},
		&Timestamp{Timestamp: 2_100},
	)

	stitched, _, err := StitchReplayStreams([][]byte{first, second})
	if err != nil {
		t.Fatalf("stitch: %v", err)
	}
	decoded, err := DecodeReplayStream(stitched)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var previous uint64
	for _, message := range decoded {
		if message.Meta().Index <= previous {
			t.Fatalf("message indexes are not monotonic: previous=%d current=%d", previous, message.Meta().Index)
		}
		previous = message.Meta().Index
	}
}

func TestStitchReplayStreamsPrefixesTabIDsPerSourceSession(t *testing.T) {
	first := testIndexedReplay(
		&Timestamp{Timestamp: 1_000},
		&TabData{TabId: "tab"},
		&Timestamp{Timestamp: 1_100},
	)
	second := testIndexedReplay(
		&Timestamp{Timestamp: 2_000},
		&TabData{TabId: "tab"},
		&Timestamp{Timestamp: 2_100},
	)

	stitched, _, err := StitchReplayStreams([][]byte{first, second})
	if err != nil {
		t.Fatalf("stitch: %v", err)
	}
	decoded, err := DecodeReplayStream(stitched)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var tabIDs []string
	for _, message := range decoded {
		if tab, ok := message.(*TabData); ok {
			tabIDs = append(tabIDs, tab.TabId)
		}
	}
	if len(tabIDs) != 2 || tabIDs[0] != "stitched-001-tab" || tabIDs[1] != "stitched-002-tab" {
		t.Fatalf("tab ids = %#v", tabIDs)
	}
}

func TestJoinReplayContinuationStripsRepeatedHeader(t *testing.T) {
	first := testIndexedReplay(&Timestamp{Timestamp: 1_000})
	second := testIndexedReplay(&Timestamp{Timestamp: 1_100})

	joined, err := JoinReplayContinuation(first, second)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if len(joined) != len(first)+len(second)-8 {
		t.Fatalf("joined length = %d, want %d", len(joined), len(first)+len(second)-8)
	}
}
