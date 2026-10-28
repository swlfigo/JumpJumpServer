// Command jumpjump-server hosts JumpJump rooms on the local network.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/libp2p/zeroconf/v2"

	"jumpjump-server/internal/server"
)

func main() {
	cfg := server.DefaultConfig()
	addr := flag.String("addr", ":7788", "listen address")
	bonjour := flag.Bool("bonjour", true, "advertise _jumpjump._tcp on the local network")
	flag.DurationVar(&cfg.IdleTimeout, "idle", cfg.IdleTimeout, "forfeit a racer after this long without jumping")
	flag.IntVar(&cfg.MinPlayers, "min-players", cfg.MinPlayers, "players needed before an all-ready room starts")
	flag.IntVar(&cfg.MaxPlayers, "max-players", cfg.MaxPlayers, "room capacity")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Error("listen", "err", err)
		os.Exit(1)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	ips := lanIPv4s()
	for _, ip := range ips {
		log.Info("reachable", "url", fmt.Sprintf("ws://%s:%d/ws", ip, port))
	}
	if *bonjour {
		service, err := advertise(port, ips)
		if err != nil {
			log.Warn("bonjour unavailable; clients must enter the address", "err", err)
		} else {
			defer service.Shutdown()
		}
	}

	hub := server.NewHub(cfg, log)
	go hub.Run(ctx)
	mux := http.NewServeMux()
	mux.Handle("GET /ws", hub)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	log.Info("listening", "addr", listener.Addr().String(), "path", "/ws", "bonjour", *bonjour)
	if err := srv.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}

// advertise publishes the service under a host name of its own. zeroconf's
// Register reuses the system host name, doubling ".local" on macOS, and would
// answer for a name the OS responder already owns.
//
// zeroconf never answers AAAA queries negatively, so resolving that host name
// with getaddrinfo stalls ~5s waiting for IPv6. Clients should connect to the
// addresses in the TXT record instead.
func advertise(port int, ips []string) (*zeroconf.Server, error) {
	if len(ips) == 0 {
		return nil, errors.New("no LAN IPv4 address")
	}
	host, _ := os.Hostname()
	host = strings.TrimSuffix(host, ".local")
	label := "jumpjump-" + strings.Map(func(r rune) rune {
		if r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return r
		}
		return '-'
	}, host)
	txt := []string{"v=0", "path=/ws", fmt.Sprintf("port=%d", port), "addrs=" + strings.Join(ips, ",")}
	return zeroconf.RegisterProxy(host, "_jumpjump._tcp", "local.", port, label, ips, txt, nil)
}

func lanIPv4s() []string {
	ifaces, _ := net.Interfaces()
	var ips []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			// Private ranges only: proxy TUN interfaces (e.g. 198.18.0.1) look
			// like LAN interfaces but swallow connections.
			if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.To4() != nil && ipNet.IP.IsPrivate() {
				ips = append(ips, ipNet.IP.String())
			}
		}
	}
	return ips
}
