package synthigy

import "testing"

func TestKebab(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"user", "user"},
		{"MusicAlbum", "music-album"},
		{"musicAlbum", "music-album"},
		{"music_album", "music-album"},
		{"music album", "music-album"},
		{"Music Album", "music-album"},
		{"already-kebab", "already-kebab"},
		{"HTTPServer", "httpserver"}, // consecutive caps stay together (no lower/digit boundary)
		{"user2Role", "user2-role"},  // digit is a boundary before a cap
		{"__leading", "leading"},     // trimmed
		{"trailing__", "trailing"},
		{"a__b", "a-b"}, // collapsed double dashes
	}
	for _, tc := range cases {
		if got := kebab(tc.in); got != tc.want {
			t.Errorf("kebab(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCollapseDashes(t *testing.T) {
	if got := collapseDashes("a---b--c"); got != "a-b-c" {
		t.Fatalf("collapseDashes = %q", got)
	}
}
