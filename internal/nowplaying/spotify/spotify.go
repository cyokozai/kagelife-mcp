// Package spotify watches Spotify playback through the Web API and writes now_playing.json.
//
// It is the Go port of tools/nowplaying-spotify/nowplaying_spotify.py and keeps its
// behaviour: the token file and now_playing.json have the same shape, so either version can
// pick up where the other stopped, and tools/conductor/wait_next.py reads the output as is.
// Auth is Authorization Code with PKCE: there is no client secret anywhere.
//
// Never log access tokens, refresh tokens, authorization codes or code verifiers.
package spotify

import "time"

const (
	// Command is the subcommand name of the kagelife-mcp executable.
	Command = "nowplaying-spotify"

	SourceName          = "spotify"
	DefaultAuthorizeURL = "https://accounts.spotify.com/authorize"
	DefaultTokenURL     = "https://accounts.spotify.com/api/token"
	DefaultAPIBase      = "https://api.spotify.com/v1"
	Scopes              = "user-read-currently-playing user-read-playback-state"
	DefaultRedirectURI  = "http://127.0.0.1:8888/callback"
	ClientIDEnv         = "KAGELIFE_SPOTIFY_CLIENT_ID"

	NextCount = 3
	// history_id rules (see README): a same-track observation is a new play only when the
	// progress went back to within StartWindowMS of the start AND the previous play would
	// have reached within EndWindowMS of its end by now.
	StartWindowMS = 15000
	EndWindowMS   = 15000
	// Refresh the access token this many seconds before it expires.
	ExpiryMarginS = 60
	MaxBackoff    = 60 * time.Second
)
