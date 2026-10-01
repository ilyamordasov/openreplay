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

func TestStitchReplayStreamsCompactsSessionGapInSingleTimeline(t *testing.T) {
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


func appendSizedTestMessage(data []byte, message Message) []byte {
	encoded := message.Encode()
	body := encoded[1:]
	data = append(data, encoded[0])
	size := len(body)
	data = append(data, byte(size), byte(size>>8), byte(size>>16))
	data = append(data, body...)
	return data
}

func TestDecodeReplayStreamAcceptsMultipleSizedBatchesInOneMobFile(t *testing.T) {
	data := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xfe}
	data = append(data, (&BatchMetadata{
		Version: 2, PageNo: 1, FirstIndex: 0, Timestamp: 1_000,
	}).Encode()...)
	data = appendSizedTestMessage(data, &Timestamp{Timestamp: 1_000})
	data = appendSizedTestMessage(data, &CreateDocument{})

	data = append(data, (&BatchMetadata{
		Version: 2, PageNo: 1, FirstIndex: 2, Timestamp: 1_500,
	}).Encode()...)
	data = appendSizedTestMessage(data, &Timestamp{Timestamp: 1_500})
	data = appendSizedTestMessage(data, &CreateDocument{})

	decoded, err := DecodeReplayStream(data)
	if err != nil {
		t.Fatalf("DecodeReplayStream() error = %v", err)
	}

	var timestamps []uint64
	var batchMetadataCount int
	for _, message := range decoded {
		switch msg := message.(type) {
		case *Timestamp:
			timestamps = append(timestamps, msg.Timestamp)
		case *BatchMetadata:
			batchMetadataCount++
		}
	}
	if batchMetadataCount != 2 {
		t.Fatalf("batch metadata count = %d, want 2", batchMetadataCount)
	}
	if len(timestamps) != 2 || timestamps[0] != 1_000 || timestamps[1] != 1_500 {
		t.Fatalf("timestamps = %#v", timestamps)
	}
}


func TestStitchReplayStreamsKeepsSingleSyntheticSessionLifecycle(t *testing.T) {
	first := testIndexedReplay(
		&Timestamp{Timestamp: 1_000},
		&SessionStart{Timestamp: 1_000, UserUUID: "fp-a"},
		&CreateDocument{},
		&Timestamp{Timestamp: 1_100},
		&SessionEnd{Timestamp: 1_100},
	)
	second := testIndexedReplay(
		&Timestamp{Timestamp: 2_000},
		&SessionStart{Timestamp: 2_000, UserUUID: "fp-a"},
		&CreateDocument{},
		&Timestamp{Timestamp: 2_100},
		&SessionEnd{Timestamp: 2_100},
	)

	stitched, _, err := StitchReplayStreams([][]byte{first, second})
	if err != nil {
		t.Fatalf("stitch: %v", err)
	}
	decoded, err := DecodeReplayStream(stitched)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	var starts, ends, documents int
	for _, message := range decoded {
		switch message.(type) {
		case *SessionStart:
			starts++
		case *SessionEnd:
			ends++
		case *CreateDocument:
			documents++
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("lifecycle starts=%d ends=%d, want one synthetic session", starts, ends)
	}
	if documents != 2 {
		t.Fatalf("CreateDocument count=%d, want 2 source DOM resets", documents)
	}
}


func TestBuildStitchPlanCompactsOverlappingSourceSessions(t *testing.T) {
	first := []Message{
		&Timestamp{Timestamp: 1_000},
		&Timestamp{Timestamp: 2_000},
	}
	second := []Message{
		&Timestamp{Timestamp: 1_500},
		&Timestamp{Timestamp: 2_500},
	}

	plan, err := BuildStitchPlan([][]Message{first, second})
	if err != nil {
		t.Fatalf("BuildStitchPlan() error = %v", err)
	}
	if len(plan) != 2 {
		t.Fatalf("plan segments = %d, want 2", len(plan))
	}
	if plan[1].TargetStart != 2_001 {
		t.Fatalf("overlapping second segment target start = %d, want 2001", plan[1].TargetStart)
	}
	if plan[1].TargetEnd != 3_001 {
		t.Fatalf("overlapping second segment target end = %d, want 3001", plan[1].TargetEnd)
	}
}
