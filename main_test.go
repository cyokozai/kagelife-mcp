package main

import "testing"

// `kagelife-mcp nowplaying-spotify ...` goes to the Spotify watcher, not to the MCP server.
func TestRunDispatchesNowplayingSpotify(t *testing.T) {
	if rc := run([]string{"nowplaying-spotify", "--help"}); rc != 0 {
		t.Fatalf("nowplaying-spotify --help: rc = %d", rc)
	}
	if rc := run([]string{"nowplaying-spotify", "--no-such-flag"}); rc != 2 {
		t.Fatalf("nowplaying-spotify --no-such-flag: rc = %d", rc)
	}
	// anything else is still the MCP server's (which rejects arguments with 2)
	if rc := run([]string{"bogus"}); rc != 2 {
		t.Fatalf("bogus: rc = %d", rc)
	}
}
