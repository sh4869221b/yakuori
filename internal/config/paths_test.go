package config

import (
	"reflect"
	"testing"
)

func TestResolve(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want Paths
		bad  bool
	}{
		{"defaults", map[string]string{"HOME": "/home/test"}, Paths{"/home/test/.config/yakuori", "/home/test/.local/share/yakuori", "/home/test/.cache/yakuori", "/home/test/.local/state/yakuori"}, false},
		{"explicit without home", map[string]string{"XDG_CONFIG_HOME": "/c", "XDG_DATA_HOME": "/d", "XDG_CACHE_HOME": "/k", "XDG_STATE_HOME": "/s"}, Paths{"/c/yakuori", "/d/yakuori", "/k/yakuori", "/s/yakuori"}, false},
		{"relative ignored", map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "relative"}, Paths{"/h/.config/yakuori", "/h/.local/share/yakuori", "/h/.cache/yakuori", "/h/.local/state/yakuori"}, false},
		{"missing home", map[string]string{}, Paths{}, true},
		{"relative home", map[string]string{"HOME": "relative"}, Paths{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(func(k string) string { return tt.env[k] })
			if (err != nil) != tt.bad {
				t.Fatalf("error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}
