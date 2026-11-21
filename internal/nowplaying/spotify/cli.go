package spotify

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const usage = `usage: kagelife-mcp nowplaying-spotify [-h] [--client-id CLIENT_ID] [--token TOKEN] [--out OUT]
                                       [--interval INTERVAL] [--once] [--redirect-uri REDIRECT_URI]
                                       [--listen-host LISTEN_HOST] [--listen-port LISTEN_PORT]
                                       [--login-timeout LOGIN_TIMEOUT] [-v]
                                       [{watch,login}]

Write the Spotify track now playing to now_playing.json

positional arguments:
  {watch,login}         watch (default) or login

options:
  -h, --help            show this help message and exit
  --client-id CLIENT_ID
                        Spotify app Client ID (default: $KAGELIFE_SPOTIFY_CLIENT_ID)
  --token TOKEN         token file (mode 0600)
  --out OUT             output JSON
  --interval INTERVAL   poll interval in seconds
  --once                read once, print the payload, do not write
  --redirect-uri REDIRECT_URI
  --listen-host LISTEN_HOST
                        login callback bind address
  --listen-port LISTEN_PORT
                        login callback port
  --login-timeout LOGIN_TIMEOUT
  -v, --verbose
`

type options struct {
	command      string
	clientID     string
	token        string
	out          string
	interval     float64
	once         bool
	redirectURI  string
	listenHost   string
	listenPort   int
	loginTimeout float64
	verbose      bool
}

// parseArgs follows the Python version's argparse: same flags, same defaults, the command
// (watch / login) before, between or after the flags. rc is -1 to go on, else the exit code.
func parseArgs(args []string, getenv func(string) string, home string, stdout, stderr io.Writer) (options, int) {
	o := options{}
	fs := flag.NewFlagSet(Command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	fs.StringVar(&o.clientID, "client-id", getenv(ClientIDEnv), "")
	fs.StringVar(&o.token, "token", filepath.Join(home, ".config", "kagelife", "spotify-token.json"), "")
	fs.StringVar(&o.out, "out", filepath.Join(home, ".cache", "kagelife", "now_playing.json"), "")
	fs.Float64Var(&o.interval, "interval", 3.0, "")
	fs.BoolVar(&o.once, "once", false, "")
	fs.StringVar(&o.redirectURI, "redirect-uri", DefaultRedirectURI, "")
	fs.StringVar(&o.listenHost, "listen-host", "0.0.0.0", "")
	fs.IntVar(&o.listenPort, "listen-port", 8888, "")
	fs.Float64Var(&o.loginTimeout, "login-timeout", 300.0, "")
	fs.BoolVar(&o.verbose, "v", false, "")
	fs.BoolVar(&o.verbose, "verbose", false, "")

	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				_, _ = fmt.Fprint(stdout, usage)
				return o, 0
			}
			_, _ = fmt.Fprint(stderr, usage)
			return o, 2
		}
		r := fs.Args()
		if len(r) == 0 {
			break
		}
		if o.command != "" {
			_, _ = fmt.Fprint(stderr, usage)
			_, _ = fmt.Fprintf(stderr, "error: unrecognized arguments: %s\n", strings.Join(r, " "))
			return o, 2
		}
		o.command, rest = r[0], r[1:]
	}
	switch o.command {
	case "":
		o.command = "watch"
	case "watch", "login":
	default:
		_, _ = fmt.Fprint(stderr, usage)
		_, _ = fmt.Fprintf(stderr, "error: argument command: invalid choice: %q (choose from 'watch', 'login')\n", o.command)
		return o, 2
	}
	return o, -1
}

// cliConfig holds what tests replace. Empty fields get the real Spotify and the real clock.
type cliConfig struct {
	AuthorizeURL string
	TokenURL     string
	APIBase      string
	HTTP         *http.Client
	Clock        func() time.Time
	Now          func() float64
	Home         string
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// Main is `kagelife-mcp nowplaying-spotify ...`. SIGTERM and SIGINT end the watch with 0.
func Main(args []string) int {
	ctx, stop := signalContext()
	defer stop()
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return runCLI(ctx, args, os.Getenv, os.Stdout, os.Stderr, cliConfig{Home: home})
}

func runCLI(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, cfg cliConfig) int {
	o, rc := parseArgs(args, getenv, cfg.Home, stdout, stderr)
	if rc >= 0 {
		return rc
	}
	log := NewLogger(stderr, o.verbose)
	if o.clientID == "" {
		log.Errorf("no Client ID: pass --client-id or set %s", ClientIDEnv)
		return 2
	}
	if o.interval <= 0 {
		log.Errorf("--interval must be > 0")
		return 2
	}
	if cfg.AuthorizeURL == "" {
		cfg.AuthorizeURL = DefaultAuthorizeURL
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = DefaultTokenURL
	}
	if cfg.APIBase == "" {
		cfg.APIBase = DefaultAPIBase
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: RequestTimeout}
	}
	if cfg.Now == nil {
		cfg.Now = func() float64 { return float64(time.Now().UnixMicro()) / 1e6 }
	}

	if o.command == "login" {
		return Login(ctx, LoginOptions{
			ClientID: o.clientID, TokenPath: o.token, ListenHost: o.listenHost, ListenPort: o.listenPort,
			RedirectURI: o.redirectURI, Timeout: seconds(o.loginTimeout), AuthorizeURL: cfg.AuthorizeURL,
			TokenURL: cfg.TokenURL, HTTP: cfg.HTTP, Now: cfg.Now, Log: log, Stdout: stdout,
		})
	}
	tm := &TokenManager{Path: o.token, ClientID: o.clientID, TokenURL: cfg.TokenURL, HTTP: cfg.HTTP, Now: cfg.Now, Log: log}
	client := &Client{Tokens: tm, HTTP: cfg.HTTP, APIBase: cfg.APIBase, Log: log}
	w := NewWatcher(client, o.out, cfg.Clock, log)
	if !o.once {
		return RunForever(ctx, w, o.out, seconds(o.interval), SleepContext, 0, log)
	}
	p, err := w.Observe(ctx)
	var lr *LoginRequiredError
	var rl *RateLimitedError
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return 0
	case errors.As(err, &lr), errors.As(err, &rl):
		log.Errorf("%s", err)
		return 1
	default:
		log.Errorf("read failed (%v)", err)
		return 1
	}
	if p == nil {
		_, _ = fmt.Fprintln(stdout, "null")
	} else {
		_, _ = fmt.Fprintln(stdout, string(Encode(p, false)))
	}
	return 0
}
