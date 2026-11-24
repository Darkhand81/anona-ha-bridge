package cam

import (
	"reflect"
	"testing"
)

func TestEventRef(t *testing.T) {
	// Shape of a captured detection push; ids and timestamp are made up.
	detection := `{"businessType":3000001,"deviceChannel":96001001,"deviceId":"00000000000000000000000000000000","deviceModule":96001,"deviceName":"Küche","deviceType":96,"eventId":"AAAAAAAAAAAAAAAA","eventTs":1790000000000}`
	tests := []struct {
		name   string
		packet []byte
		id     string
		ts     int64
	}{
		{"json detection", []byte(detection), "AAAAAAAAAAAAAAAA", 1790000000000},
		{"json without eventId", []byte(`{"businessType":3000001}`), "", 0},
		{"broken json", []byte(`{"eventId":`), "", 0},
		{"protobuf status frame", []byte{0x08, 0x32, 0x10, 0x03}, "", 0},
		{"empty", nil, "", 0},
	}
	for _, tt := range tests {
		if id, ts := EventRef(tt.packet); id != tt.id || ts != tt.ts {
			t.Errorf("%s: got %q %d, want %q %d", tt.name, id, ts, tt.id, tt.ts)
		}
	}
	if got := ParseEventTags([]byte(detection)); got != nil {
		t.Errorf("ParseEventTags(json) = %v, want nil", got)
	}
}

func TestTagNames(t *testing.T) {
	tests := []struct {
		tags []string
		want []string
	}{
		{[]string{"human"}, []string{"person"}},
		{[]string{"pet", "human"}, []string{"animal", "person"}},
		{[]string{"Human", "person"}, []string{"person"}},
		{[]string{"5"}, []string{"animal"}},
		{[]string{"unknown"}, []string{"motion"}},
		{nil, []string{"motion"}},
	}
	for _, tt := range tests {
		if got := TagNames(tt.tags); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("TagNames(%v) = %v, want %v", tt.tags, got, tt.want)
		}
	}
}
