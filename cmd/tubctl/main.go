// tubctl — self-hosted LAN-only control for Bestway Airjet hot tubs.
//
// Speaks the Gizwits GAgent LAN protocol directly to the tub on the LAN;
// no cloud, no Bestway account.
//
// Subcommands:
//
//	tubctl serve         start the HTTP server + web UI (default in container)
//	tubctl state         print current tub state once and exit
//	tubctl set key=val   write one or more attributes and verify
//	tubctl watch         poll status continuously and show diffs
package main

import (
	"fmt"
	"os"

	// Embed the IANA timezone database in the binary. The runtime image is
	// FROM scratch with no /usr/share/zoneinfo, so without this `time.Local`
	// (and any TZ=Europe/Oslo) silently falls back to UTC — which would make
	// the daily scheduler fire windows at the wrong wall-clock time.
	_ "time/tzdata"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "serve":
		runServe(args)
	case "state":
		runState(args)
	case "set":
		runSet(args)
	case "watch":
		runWatch(args)
	case "help", "-h", "--help":
		usage()
	case "version", "-v", "--version":
		fmt.Println("tubctl", version)
	default:
		fmt.Fprintf(os.Stderr, "tubctl: unknown subcommand %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

// version is overridden at build time via -ldflags="-X main.version=v0.1.2".
var version = "dev"

func usage() {
	fmt.Fprint(os.Stderr, `tubctl — LAN-only control for Bestway Airjet hot tubs.

USAGE
  tubctl <subcommand> [args]

SUBCOMMANDS
  serve              start the HTTP server + web UI on $PORT (default 3000)
  state              read current tub state and print it
  set k=v [k=v...]   write attributes; values: bool=0/1, temp_set=20-40,
                     *_appm_min and *_timer_min = uint16 minutes
  watch              continuously poll status and print diffs
  version            print version
  help               this help

WRITABLE ATTRIBUTES
  power heat_power filter_power wave_power locked earth temp_set_unit
  temp_set heat_appm_min heat_timer_min filter_appm_min filter_timer_min
  wave_appm_min wave_timer_min

ENVIRONMENT
  TUB_HOST           tub IP on the LAN (default 172.31.0.105)
  TUB_PORT           tub TCP port (default 12416)
  PORT               HTTP server port for 'serve' (default 3000)
  TIME_FORMAT        "24" or "12" — UI clock format (default 24)
  TZ                  IANA timezone for the scheduler, e.g. Europe/Oslo
                     (default UTC; the zone database is embedded in the binary)
  DATA_DIR           where recurring schedules persist (default ./data)
  AUTH_TOKEN         if set, write endpoints require this token via
                     X-Auth-Token or Authorization: Bearer (default off)
  ALLOWED_HOSTS      comma-separated Host allowlist for write endpoints,
                     defeats DNS rebinding (default off — any host)
  LOG_LEVEL          debug|info|warn|error (default info)

EXAMPLES
  tubctl state
  tubctl set heat_power=1 temp_set=38
  tubctl set locked=0
  tubctl watch
  TUB_HOST=192.168.1.50 tubctl serve
`)
}
