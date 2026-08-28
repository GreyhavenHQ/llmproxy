// llmproxy: serve the proxy, or bootstrap principals and keys directly in the
// database. The CLI is how the very first admin key is minted in local mode;
// after that, everything can be done over the HTTP API.
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
	"strconv"
	"syscall"
	"time"

	"github.com/greyhavenhq/llmproxy/internal/config"
	"github.com/greyhavenhq/llmproxy/internal/secrets"
	"github.com/greyhavenhq/llmproxy/internal/server"
	"github.com/greyhavenhq/llmproxy/internal/store"
)

// Stamped at build time with -ldflags "-X main.version=... -X main.gitCommit=...
// -X main.buildTime=...". Release builds get the tag; a plain `go build` leaves
// the defaults, which is why an unstamped binary honestly reports "dev".
var (
	version   = "dev"
	gitCommit = ""
	buildTime = ""
)

// versionString renders the stamped build metadata, omitting whatever was not
// stamped: "dev", "v1.1.0 (abc1234)" or "v1.1.0 (abc1234, built <time>)".
func versionString(version, gitCommit, buildTime string) string {
	switch {
	case gitCommit == "" && buildTime == "":
		return version
	case buildTime == "":
		return fmt.Sprintf("%s (%s)", version, gitCommit)
	case gitCommit == "":
		return fmt.Sprintf("%s (built %s)", version, buildTime)
	default:
		return fmt.Sprintf("%s (%s, built %s)", version, gitCommit, buildTime)
	}
}

var loopbackHosts = map[string]bool{"127.0.0.1": true, "::1": true, "localhost": true}

const nonlocalWarning = `!! WARNING: local (no-SSO) mode is bound to a non-loopback interface.
!! Anyone who can reach this address can use any key minted here.
!! Set up OIDC before exposing this to a network you do not fully trust.`

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(os.Args[2:])
	case "key":
		err = cmdKey(os.Args[2:])
	case "relay-token":
		err = cmdRelayToken(os.Args[2:])
	case "principal":
		err = cmdPrincipal(os.Args[2:])
	case "version", "--version":
		fmt.Println(versionString(version, gitCommit, buildTime))
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: llmproxy <command>

commands:
  serve                     run the proxy
  key create|list|delete    manage API keys directly in the database
  relay-token create|list|delete
                            manage transparent-relay tokens
  principal create          create a user or service principal
  version                   print the version`)
}

func openStore(cfg config.Config) (*store.Store, error) {
	st, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := st.Init(ctx); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	host := fs.String("host", "", "bind address (default from LLMPROXY_HOST)")
	port := fs.Int("port", 0, "bind port (default from LLMPROXY_PORT)")
	allowNonlocal := fs.Bool("allow-nonlocal", false, "allow binding outside loopback in local (no-SSO) mode")
	_ = fs.Parse(args)

	cfg := config.FromEnv()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.SlogLevel()})))
	if *host != "" {
		cfg.Host = *host
	}
	if *port != 0 {
		cfg.Port = *port
	}
	if *allowNonlocal {
		cfg.AllowNonlocal = true
	}
	if cfg.LocalMode() && !loopbackHosts[cfg.Host] {
		if !cfg.AllowNonlocal {
			return fmt.Errorf(
				"refusing to bind local (no-SSO) mode to '%s'; use --allow-nonlocal (or LLMPROXY_ALLOW_NONLOCAL=1) to override",
				cfg.Host)
		}
		fmt.Fprintln(os.Stderr, nonlocalWarning)
	}

	secret, err := secrets.LoadOrCreate(cfg.KeySecret, cfg.SecretFile)
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	srv := server.New(cfg, st, secret)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = srv.Bootstrap(ctx)
	cancel()
	if err != nil {
		return err
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	httpServer := &http.Server{Addr: addr, Handler: srv.Handler()}
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-shutdown
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}()
	fmt.Fprintf(os.Stderr, "llmproxy %s listening on http://%s\n", version, addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	srv.Drain()
	return nil
}

func cmdKey(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: llmproxy key create|list|revoke")
	}
	cfg := config.FromEnv()
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("key create", flag.ExitOnError)
		label := fs.String("label", "", "key label")
		principalName := fs.String("principal", "", "principal name (default: the local admin)")
		_ = fs.Parse(args[1:])

		secret, err := secrets.LoadOrCreate(cfg.KeySecret, cfg.SecretFile)
		if err != nil {
			return err
		}
		var principal *store.Principal
		if *principalName == "" && cfg.LocalMode() {
			principal, err = st.GetOrCreatePrincipal(ctx, cfg.LocalAdminName, "user", "admin", nil)
		} else {
			name := *principalName
			if name == "" {
				name = cfg.LocalAdminName
			}
			principal, err = st.GetPrincipalByName(ctx, name)
			if err == nil && principal == nil {
				return fmt.Errorf("principal '%s' does not exist; create it first with 'llmproxy principal create'", name)
			}
		}
		if err != nil {
			return err
		}
		plaintext := secrets.GenerateAPIKey()
		key, err := st.CreateAPIKey(ctx, principal.ID,
			secrets.HashAPIKey(secret, plaintext), secrets.KeySuffix(plaintext), *label, nil)
		if err != nil {
			return err
		}
		fmt.Printf("key id:    %s\nprincipal: %s\napi key:   %s\n", key.ID, principal.Name, plaintext)
		fmt.Println("Store it now; it will not be shown again.")
		return nil

	case "list":
		keys, err := st.ListAPIKeys(ctx, "", "", 500, 0)
		if err != nil {
			return err
		}
		for _, k := range keys {
			fmt.Printf("%s  %-20s %-20s ***%-6s created=%s\n",
				k.ID, k.PrincipalName, k.Label, k.Suffix, k.CreatedAt[:10])
		}
		return nil

	case "delete", "revoke": // revoke kept as an alias; deletion is the mechanism
		if len(args) < 2 {
			return errors.New("usage: llmproxy key delete <key-id>")
		}
		key, err := st.GetAPIKey(ctx, args[1], "")
		if err != nil {
			return err
		}
		if key == nil {
			return fmt.Errorf("no key with id '%s'", args[1])
		}
		if err := st.DeleteAPIKey(ctx, key.ID, nil); err != nil {
			return err
		}
		fmt.Printf("deleted %s\n", key.ID)
		return nil
	}
	return fmt.Errorf("unknown key subcommand '%s'", args[0])
}

func cmdRelayToken(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: llmproxy relay-token create|list|delete")
	}
	cfg := config.FromEnv()
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("relay-token create", flag.ExitOnError)
		label := fs.String("label", "", "token label")
		principalName := fs.String("principal", "", "principal name (default: the local admin)")
		_ = fs.Parse(args[1:])

		secret, err := secrets.LoadOrCreate(cfg.KeySecret, cfg.SecretFile)
		if err != nil {
			return err
		}
		name := *principalName
		if name == "" {
			name = cfg.LocalAdminName
		}
		principal, err := st.GetPrincipalByName(ctx, name)
		if err != nil {
			return err
		}
		if principal == nil {
			return fmt.Errorf("principal '%s' does not exist; create it first with 'llmproxy principal create'", name)
		}
		plaintext := secrets.GenerateRelayToken()
		token, err := st.CreateRelayToken(ctx, principal.ID,
			secrets.HashAPIKey(secret, plaintext), secrets.KeySuffix(plaintext), *label, nil)
		if err != nil {
			return err
		}
		fmt.Printf("token id:    %s\nprincipal:   %s\nrelay token: %s\n", token.ID, principal.Name, plaintext)
		fmt.Printf("base url:    http://%s/transparent/anthropic/%s\n",
			net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)), plaintext)
		fmt.Println("Store it now; it will not be shown again.")
		return nil

	case "list":
		tokens, err := st.ListRelayTokens(ctx, "", 500, 0)
		if err != nil {
			return err
		}
		for _, rt := range tokens {
			fmt.Printf("%s  %-20s %-20s ***%-6s created=%s\n",
				rt.ID, rt.PrincipalName, rt.Label, rt.Suffix, rt.CreatedAt[:10])
		}
		return nil

	case "delete":
		if len(args) < 2 {
			return errors.New("usage: llmproxy relay-token delete <token-id>")
		}
		token, err := st.GetRelayToken(ctx, args[1], "")
		if err != nil {
			return err
		}
		if token == nil {
			return fmt.Errorf("no relay token with id '%s'", args[1])
		}
		if err := st.DeleteRelayToken(ctx, token.ID, nil); err != nil {
			return err
		}
		fmt.Printf("deleted %s\n", token.ID)
		return nil
	}
	return fmt.Errorf("unknown relay-token subcommand '%s'", args[0])
}

func cmdPrincipal(args []string) error {
	if len(args) < 1 || args[0] != "create" {
		return errors.New("usage: llmproxy principal create -name <name> [-kind user|service] [-role member|admin]")
	}
	fs := flag.NewFlagSet("principal create", flag.ExitOnError)
	name := fs.String("name", "", "principal name")
	kind := fs.String("kind", "user", "user | service")
	role := fs.String("role", "member", "member | admin")
	_ = fs.Parse(args[1:])
	if *name == "" {
		return errors.New("-name is required")
	}
	if (*kind != "user" && *kind != "service") || (*role != "member" && *role != "admin") {
		return errors.New("kind must be user|service, role must be member|admin")
	}
	cfg := config.FromEnv()
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	existing, err := st.GetPrincipalByName(ctx, *name)
	if err != nil {
		return err
	}
	if existing != nil {
		return fmt.Errorf("principal '%s' already exists", *name)
	}
	principal, err := st.GetOrCreatePrincipal(ctx, *name, *kind, *role, nil)
	if err != nil {
		return err
	}
	fmt.Printf("created principal '%s' (%s, %s)\n", principal.Name, principal.Kind, principal.Role)
	return nil
}
