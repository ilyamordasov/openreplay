package messages

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"reflect"
)

var ErrReplayStreamHasNoTimestamps = errors.New("replay stream has no timestamps")

type StitchSegment struct {
	SourceStart uint64
	SourceEnd   uint64
	TargetStart uint64
	TargetEnd   uint64
	Shift       int64
}

func replayHeader(data []byte) (byte, []byte, bool) {
	if len(data) < 8 {
		return 0, data, false
	}
	for i := 0; i < 7; i++ {
		if data[i] != 0xff {
			return 0, data, false
		}
	}
	switch data[7] {
	case 0xff, 0xfe, 0xfd:
		return data[7], data[8:], true
	default:
		return 0, data, false
	}
}

func decodeIndexedLegacyStream(data []byte) ([]Message, error) {
	reader := NewBytesReader(data)
	decoded := make([]Message, 0)
	for reader.Pointer() < int64(len(data)) {
		index, err := reader.ReadIndex()
		if err != nil {
			return nil, fmt.Errorf("read replay message index: %w", err)
		}
		msgType, err := reader.ReadUint()
		if err != nil {
			return nil, fmt.Errorf("read replay message type: %w", err)
		}
		msg, err := ReadMessage(msgType, reader)
		if err != nil {
			return nil, fmt.Errorf("decode replay message %d: %w", msgType, err)
		}
		msg = transformDeprecated(msg)
		msg.Meta().Index = index
		decoded = append(decoded, msg)
	}
	if len(decoded) == 0 {
		return nil, errors.New("empty indexed replay stream")
	}
	return decoded, nil
}

func decodeLegacyStream(data []byte) ([]Message, error) {
	reader := NewBytesReader(data)
	decoded := make([]Message, 0)
	for reader.Pointer() < int64(len(data)) {
		msgType, err := reader.ReadUint()
		if err != nil {
			return nil, fmt.Errorf("read replay message type: %w", err)
		}
		msg, err := ReadMessage(msgType, reader)
		if err != nil {
			return nil, fmt.Errorf("decode replay message %d: %w", msgType, err)
		}
		decoded = append(decoded, transformDeprecated(msg))
	}
	if len(decoded) == 0 {
		return nil, errors.New("empty replay stream")
	}
	return decoded, nil
}

func decodeSizedStream(data []byte) ([]Message, error) {
	// A stored mob file is a concatenation of tracker batches. Each batch may
	// start with its own BatchMetadata, so parse framing directly instead of
	// MessageReader.Parse, which validates one batch at a time.
	reader := NewBytesReader(data)
	decoded := make([]Message, 0)
	var version uint64
	var index uint64

	for reader.Pointer() < int64(len(data)) {
		msgType, err := reader.ReadUint()
		if err != nil {
			return nil, fmt.Errorf("read sized replay message type: %w", err)
		}

		if msgType == MsgBatchMetadata {
			msg, err := DecodeBatchMetadata(reader)
			if err != nil {
				return nil, fmt.Errorf("decode replay batch metadata: %w", err)
			}
			meta := msg.(*BatchMetadata)
			if meta.Version < 1 || meta.Version > 5 {
				return nil, fmt.Errorf("unsupported replay batch version %d", meta.Version)
			}
			version = meta.Version
			index = meta.PageNo<<32 + meta.FirstIndex
			msg.Meta().Index = index
			decoded = append(decoded, msg)
			continue
		}

		if version == 0 {
			return nil, errors.New("sized replay message before batch metadata")
		}

		size, err := reader.ReadSize()
		if err != nil {
			return nil, fmt.Errorf("read sized replay message size: %w", err)
		}
		start := reader.Pointer()
		end := start + int64(size)
		if end > int64(len(data)) {
			return nil, fmt.Errorf("sized replay message %d exceeds stream bounds", msgType)
		}

		bodyReader := NewBytesReader(data[start:end])
		msg, err := ReadMessage(msgType, bodyReader)
		if err != nil {
			return nil, fmt.Errorf("decode sized replay message %d: %w", msgType, err)
		}
		if bodyReader.Pointer() > int64(size) {
			return nil, fmt.Errorf("decoded replay message %d beyond declared size", msgType)
		}
		reader.SetPointer(end)
		index++
		msg = transformDeprecated(msg)
		msg.Meta().Index = index
		decoded = append(decoded, msg)
	}

	if len(decoded) == 0 {
		return nil, errors.New("empty sized replay stream")
	}
	return decoded, nil
}

// DecodeReplayStream accepts the on-object-storage representation used by
// OpenReplay sink: v1 indexed streams and v2/v3 sized tracker streams.
func DecodeReplayStream(data []byte) ([]Message, error) {
	marker, body, hasHeader := replayHeader(data)
	if hasHeader {
		switch marker {
		case 0xff:
			if messages, err := decodeIndexedLegacyStream(body); err == nil {
				return messages, nil
			}
			return decodeLegacyStream(body)
		case 0xfe, 0xfd:
			return decodeSizedStream(body)
		}
	}

	// Continuation files may not repeat the eight-byte header.
	if messages, err := decodeIndexedLegacyStream(body); err == nil {
		return messages, nil
	}
	if messages, err := decodeSizedStream(body); err == nil {
		return messages, nil
	}
	return decodeLegacyStream(body)
}

func directTimestamp(message Message) (uint64, bool) {
	value := reflect.ValueOf(message)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return 0, false
	}
	value = value.Elem()
	typ := value.Type()
	for i := 0; i < value.NumField(); i++ {
		fieldType := typ.Field(i)
		if fieldType.Name != "Timestamp" || fieldType.PkgPath != "" {
			continue
		}
		field := value.Field(i)
		switch field.Kind() {
		case reflect.Uint64, reflect.Uint, reflect.Uint32:
			ts := field.Uint()
			return ts, ts != 0
		case reflect.Int64, reflect.Int, reflect.Int32:
			ts := field.Int()
			if ts > 0 {
				return uint64(ts), true
			}
		}
	}
	return 0, false
}

func timestampBounds(messages []Message) (uint64, uint64, error) {
	var minTs uint64
	var maxTs uint64
	found := false
	for _, message := range messages {
		ts, ok := directTimestamp(message)
		if !ok {
			continue
		}
		if !found || ts < minTs {
			minTs = ts
		}
		if !found || ts > maxTs {
			maxTs = ts
		}
		found = true
	}
	if !found {
		return 0, 0, ErrReplayStreamHasNoTimestamps
	}
	return minTs, maxTs, nil
}

func shiftedTimestamp(ts uint64, shift int64) (uint64, error) {
	if shift >= 0 {
		return ts + uint64(shift), nil
	}
	amount := uint64(-shift)
	if amount > ts {
		return 0, errors.New("timestamp shift underflow")
	}
	return ts - amount, nil
}

func shiftMessageTimestamps(message Message, shift int64) error {
	if shift == 0 {
		return nil
	}

	meta := message.Meta()
	if meta != nil && meta.Timestamp != 0 {
		shifted, err := shiftedTimestamp(meta.Timestamp, shift)
		if err != nil {
			return err
		}
		meta.Timestamp = shifted
	}

	value := reflect.ValueOf(message)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return nil
	}
	value = value.Elem()
	typ := value.Type()
	for i := 0; i < value.NumField(); i++ {
		fieldType := typ.Field(i)
		if fieldType.Name != "Timestamp" || fieldType.PkgPath != "" {
			continue
		}
		field := value.Field(i)
		if !field.CanSet() {
			continue
		}
		switch field.Kind() {
		case reflect.Uint64, reflect.Uint, reflect.Uint32:
			ts := field.Uint()
			if ts == 0 {
				continue
			}
			shifted, err := shiftedTimestamp(ts, shift)
			if err != nil {
				return err
			}
			field.SetUint(shifted)
		case reflect.Int64, reflect.Int, reflect.Int32:
			ts := field.Int()
			if ts <= 0 {
				continue
			}
			shifted, err := shiftedTimestamp(uint64(ts), shift)
			if err != nil {
				return err
			}
			field.SetInt(int64(shifted))
		}
	}
	return nil
}

func prefixSegmentTabIDs(message Message, segment int) {
	prefix := fmt.Sprintf("stitched-%03d-", segment+1)
	switch msg := message.(type) {
	case *TabData:
		msg.TabId = prefix + msg.TabId
	case *TabChange:
		msg.TabId = prefix + msg.TabId
	}
}

func encodeCanonicalReplay(segments [][]Message, plan []StitchSegment) ([]byte, error) {
	if len(segments) != len(plan) {
		return nil, errors.New("stitch plan does not match replay segments")
	}

	var output bytes.Buffer
	// Canonical export uses the legacy indexed representation because it can
	// hold messages decoded from every supported tracker batch version.
	output.Write([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})

	var index uint64 = 1
	var indexBytes [8]byte
	for segmentIndex, segment := range segments {
		for _, message := range segment {
			// Batch metadata controls transport framing, not replay semantics.
			// The stitched output has one canonical framing and does not retain it.
			if message.TypeID() == MsgBatchMetadata || message.TypeID() == MsgMobileBatchMeta {
				continue
			}
			// One stitched export is one synthetic session: preserve the first
			// SessionStart and the last SessionEnd only. CreateDocument/page
			// messages still reset DOM state between source sessions.
			if segmentIndex > 0 && message.TypeID() == MsgSessionStart {
				continue
			}
			if segmentIndex < len(segments)-1 && message.TypeID() == MsgSessionEnd {
				continue
			}
			if err := shiftMessageTimestamps(message, plan[segmentIndex].Shift); err != nil {
				return nil, fmt.Errorf("shift segment %d timestamp: %w", segmentIndex, err)
			}
			prefixSegmentTabIDs(message, segmentIndex)

			binary.LittleEndian.PutUint64(indexBytes[:], index)
			if _, err := output.Write(indexBytes[:]); err != nil {
				return nil, err
			}
			encoded := message.Encode()
			if len(encoded) == 0 {
				return nil, fmt.Errorf("empty encoded replay message type %d", message.TypeID())
			}
			if _, err := output.Write(encoded); err != nil {
				return nil, err
			}
			index++
		}
	}
	return output.Bytes(), nil
}

func BuildStitchPlan(segments [][]Message) ([]StitchSegment, error) {
	plan := make([]StitchSegment, 0, len(segments))
	var previousTargetEnd uint64

	for i, segment := range segments {
		sourceStart, sourceEnd, err := timestampBounds(segment)
		if err != nil {
			return nil, fmt.Errorf("segment %d: %w", i, err)
		}

		targetStart := sourceStart
		if i > 0 {
			// A merged replay is a compacted synthetic timeline: wall-clock gaps
			// between source sessions are intentionally removed. Keep only a 1ms
			// boundary so the next document/session can initialize cleanly.
			targetStart = previousTargetEnd + 1
		}

		shift := int64(targetStart) - int64(sourceStart)
		targetEnd := targetStart + (sourceEnd - sourceStart)
		plan = append(plan, StitchSegment{
			SourceStart: sourceStart,
			SourceEnd:   sourceEnd,
			TargetStart: targetStart,
			TargetEnd:   targetEnd,
			Shift:       shift,
		})
		previousTargetEnd = targetEnd
	}
	return plan, nil
}

func StitchReplayStreams(streams [][]byte) ([]byte, []StitchSegment, error) {
	segments := make([][]Message, 0, len(streams))
	for i, stream := range streams {
		messages, err := DecodeReplayStream(stream)
		if err != nil {
			return nil, nil, fmt.Errorf("decode replay segment %d: %w", i, err)
		}
		segments = append(segments, messages)
	}
	plan, err := BuildStitchPlan(segments)
	if err != nil {
		return nil, nil, err
	}
	stitched, err := encodeCanonicalReplay(segments, plan)
	if err != nil {
		return nil, nil, err
	}
	return stitched, plan, nil
}

func StitchReplayStreamsWithPlan(streams [][]byte, plan []StitchSegment) ([]byte, error) {
	if len(streams) != len(plan) {
		return nil, errors.New("stitch plan does not match replay streams")
	}
	segments := make([][]Message, 0, len(streams))
	for i, stream := range streams {
		if len(stream) == 0 {
			segments = append(segments, nil)
			continue
		}
		decoded, err := DecodeReplayStream(stream)
		if err != nil {
			return nil, fmt.Errorf("decode replay segment %d: %w", i, err)
		}
		segments = append(segments, decoded)
	}
	return encodeCanonicalReplay(segments, plan)
}

func JoinReplayContinuation(parts ...[]byte) ([]byte, error) {
	var output bytes.Buffer
	wroteHeader := false
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		_, body, hasHeader := replayHeader(part)
		if !wroteHeader {
			if hasHeader {
				if _, err := output.Write(part); err != nil {
					return nil, err
				}
				wroteHeader = true
				continue
			}
			// Headerless first part is still accepted by DecodeReplayStream.
			if _, err := output.Write(part); err != nil {
				return nil, err
			}
			wroteHeader = true
			continue
		}
		if hasHeader {
			part = body
		}
		if _, err := output.Write(part); err != nil {
			return nil, err
		}
	}
	if output.Len() == 0 {
		return nil, io.EOF
	}
	return output.Bytes(), nil
}
