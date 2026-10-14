package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(5) // usage error
	}
	dispatch(os.Args[1], os.Args[2:])
}

// localCmds are the verbs that never talk to the server.
var localCmds = map[string]func(Args){
	"init":      cmdInit,
	"whoami":    func(Args) { cmdWhoami() },
	"version":   func(Args) { cmdVersion() },
	"--version": func(Args) { cmdVersion() },
	"-v":        func(Args) { cmdVersion() },
	"update":    cmdUpdate,
	"upgrade":   cmdUpdate,
	"db":        cmdDB,
}

// clientCmds are the verbs that call `am serve` through a Client.
var clientCmds = map[string]func(*Client, Args){
	"ls":         cmdLs,
	"list":       cmdLs,
	"show":       cmdShow,
	"new":        cmdNew,
	"claim":      cmdClaim,
	"next":       cmdNext,
	"wait":       cmdWait,
	"status":     cmdStatus,
	"assign":     cmdAssign,
	"note":       cmdNote,
	"comment":    cmdNote,
	"edit":       cmdEdit,
	"drop":       cmdDrop,
	"rm":         cmdRm,
	"projects":   cmdProjects,
	"project":    cmdProject,
	"categories": cmdCategories,
	"category":   cmdCategory,
	"token":      cmdToken,
	"dep":        cmdDep,
	"help":       func(*Client, Args) { usage() },
	"-h":         func(*Client, Args) { usage() },
	"--help":     func(*Client, Args) { usage() },
}

// dispatch runs one verb with the arguments after it.
func dispatch(cmd string, rest []string) {
	if cmd == "serve" {
		runServe(rest)
		return
	}
	if cmd == "label" {
		// Raw argv — parse() would swallow a "-bar" removal token as a value flag.
		cmdLabel(NewClient(), rest)
		return
	}

	if cmd == "show" {
		rest = rewriteShowComments(rest)
	}

	a := parse(rest)
	if f, ok := localCmds[cmd]; ok {
		f(a)
		return
	}
	c := NewClient()
	f, ok := clientCmds[cmd]
	if !ok {
		usageErr("unknown command: %s (try `am help`)", cmd)
	}
	f(c, a)
}

// serveConfig resolves serve's port, DB path and proposals carve-out project:
// flag, then env, then default.
func serveConfig(a Args) (port, dbPath, proposals string) {
	if port = a.flag("port"); port == "" {
		port = envOr("AGENTMAN_PORT", "8787")
	}
	dbPath = dbPathFromArgs(a) // shared with `am db`, so both act on the same file
	if proposals = a.flag("proposals"); proposals == "" {
		proposals = envOr("AGENTMAN_PROPOSALS", "meta/proposals")
	}
	return port, dbPath, proposals
}

// proposalsScope parses the proposals carve-out project. Both segments are
// required — a bare category would turn the carve-out into a category-wide hole.
func proposalsScope(raw string) (Scope, error) {
	sc, err := parseScope(raw)
	if err == nil && sc.Project == "" {
		err = errors.New("missing project")
	}
	return sc, err
}

func runServe(argv []string) {
	a := parse(argv)
	port, dbPath, prop := serveConfig(a)
	// Bind before touching the DB: a busy port (e.g. a second serve) must fail
	// before OpenStore runs schema.sql on a file another server uses.
	ln, err := net.Listen("tcp", listenAddr(port))
	if err != nil {
		fail(1, "agentman: serve: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		fail(1, "agentman: %v", err)
	}
	store, err := OpenStore(dbPath)
	if err != nil {
		fail(1, "agentman: open db: %v", err)
	}

	srv := NewServer(store)
	psc, err := proposalsScope(prop)
	if err != nil {
		fail(1, "agentman: bad proposals project %q (want category/project)", prop)
	}
	srv.proposals = psc
	if a.has("log") || envOr("AGENTMAN_LOG", "") != "" {
		srv.logRequests = true
		log.Printf("agentman: request logging enabled")
	}
	baseCtx, cancelBase := context.WithCancel(context.Background())
	httpServer := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	checkForUpdate() // non-blocking; logs once if a newer version is published

	log.Printf("agentman: dashboard on http://%s   (db: %s)", ln.Addr(), dbPath)
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(ln) }()

	// A signal and a Serve failure leave through the same cleanup below.
	var failed error
	select {
	case <-ctx.Done():
		log.Printf("agentman: shutting down")
	case failed = <-serveErr: // never ErrServerClosed: Shutdown has not run yet
	}
	cancelBase() // unblock long-lived SSE handlers
	shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutCtx); err != nil {
		log.Printf("agentman: shutdown: %v", err)
	}
	if err := store.Close(); err != nil {
		log.Printf("agentman: close db: %v", err)
	}
	if failed != nil {
		fail(1, "agentman: serve: %v", failed)
	}
}

// rewriteShowComments rewrites -c tokens to --comments for `am show` only:
// -c is the global category flag (canonFlag), but show documents `am show
// <id> -c` as the comments toggle. Runs before parse() so the alias never
// reaches canonFlag.
func rewriteShowComments(rest []string) []string {
	out := make([]string, len(rest))
	for i, tok := range rest {
		if tok == "-c" {
			out[i] = "--comments"
		} else {
			out[i] = tok
		}
	}
	return out
}

// listenAddr is the server bind address. It is pinned to the 127.0.0.1 loopback
// interface — there is no authentication, so the bind is the only access control
// (see security.md). Tests guard that this never widens beyond loopback.
func listenAddr(port string) string { return "127.0.0.1:" + port }

func defaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "agentman.db"
	}
	return filepath.Join(home, ".agentman", "agentman.db")
}

func usage() {
	fmt.Print(`agentman (am) — a tiny ticketing board for agents

  am serve [--port 8787] [--db PATH] [--log] [--proposals CAT/PROJ]   run the dashboard + API

  am init <tasktype> [-c CAT [-p PROJ]]  set this session's identity, optionally scoped to a category or project
  am whoami                              print the current identity (+ scope when set)

  am ls [--mine] [--status S] [-p P] [-c CAT] [--all] [--ready] [--blocked] [--stale DUR] [--grep TEXT] [--label L] [--meta KEY] [--limit N]   list tasks (hides done; --all = every task board-wide, ignores -p/-c; --limit default 50, max 500 (0 = max); alias: list)
  am show <id> [-c]                            task detail (+comments +deps)
  am new "title" [--body B] [-p P] [--priority N] [--assign A] [--meta k=v]...   create, prints id
  am claim <id> [--steal-stale DUR]           assign me + ->doing (atomic; DUR is Go syntax, e.g. 30m, 48h)
  am next [-p P] [-c CAT] [--meta KEY]        atomically pick + claim the best ready task (prints id; exit 3 if none)
  am wait <id> --done | am wait --ready [-p P] [-c CAT] [--meta KEY] [--timeout D]   block until done / a ready task exists (exit 7 on timeout)
  am status <id...> <todo|doing|blocked|done> change status (multiple ids allowed)
  am assign <id...> <agent|me|->              reassign ("-" = unassign; multiple ids allowed)
  am note <id> "text"                         add a comment
  am edit <id> [--title T] [--body B] [--priority N] [--meta k=v]...   (--meta is repeatable; --meta k= removes the key)
  am drop <id>                                release back to todo
  am rm <id>                                  hard-delete a task (permanent)
  am dep add <id> <prereq> [prereq…]          add prerequisite(s) to a task
  am dep rm <id> <prereq>                     remove a prerequisite
  am label <id> [+l ...] [-l ...]             print / add / remove labels (lowercase a-z 0-9 . _ -)
  am projects [--all]                    list projects (--all includes archived)
  am project new <slug> [name] -c <category>  create a project (category required)
  am project edit <slug> [--slug NEW] [--name N] [--vault-id X] [--vault-path Y]   rename / set vault binding
  am project archive <slug>              soft-archive a project (hides it)
  am project unarchive <slug>            restore an archived project
  am project rm <slug> --yes             hard-delete a project + ALL its tasks/comments
  am categories [--all]                  list categories (--all includes archived)
  am category new <slug> [name]               create a category
  am category archive <slug>             soft-archive a category (hides its projects)
  am category unarchive <slug>           restore an archived category
  am token new --scope <cat[/proj]>           mint a scope-bound bearer token (prints it once; stored in identity)
                                              token new/ls/revoke need an identity with no token: run them elsewhere
                                              than where a token is stored (or set AGENTMAN_AGENT_FILE)
  am token ls [--json]                        list tokens (id / scope / created / [revoked])
  am token revoke <id>                        revoke a token (silent success)
  am version                                  print version (also --version, -v)
  am update [version]                         reinstall the latest (or a given) version (alias: upgrade)
  am db export [path] [--db PATH]                            export a DB snapshot (prints path)
  am db import <path> [--db PATH] [--yes]                    import a snapshot (stop serve first)
  am db prune [--db PATH] (--before YYYY-MM-DD | --keep N) [--yes]  delete old events rows

Identity: run 'am init <tasktype>' once per session (or set AGENTMAN_AGENT).
Scope: 'am init <tasktype> -c CAT [-p PROJ]' confines this identity — out-of-scope
     mutations/reads are rejected (exit 8); creating tasks in the proposals
     project (default meta/proposals) is allowed from any scope.
Env: AGENTMAN_URL (default http://127.0.0.1:8787), AGENTMAN_PROJECT (default project),
     AGENTMAN_AGENT (override the identity file's agent id),
     AGENTMAN_AGENT_FILE (use this identity file instead of the per-directory one),
     AGENTMAN_CATEGORY (default category scope for ls/next/wait/project new),
     AGENTMAN_SCOPE (override the identity file's scope, e.g. work or work/api),
     AGENTMAN_TOKEN (override the identity file's bearer token; sent as Authorization: Bearer),
     AGENTMAN_PORT, AGENTMAN_DB, AGENTMAN_PROPOSALS (serve: defaults for --port, --db,
     --proposals), AGENTMAN_LOG (serve: any value = --log),
     AGENTMAN_NO_UPDATE_CHECK (serve: any value skips the startup update check).
     Add --json to any read to parse output.
Args: '--' ends the flags (am note 12 -- "-x done"); --flag=VALUE passes a value starting with '-'.
Exit codes: 0 ok · 3 not found · 4 already claimed · 5 invalid (incl. any usage error) · 6 server down · 7 timed out · 8 out of scope · 9 bad token · 1 other.
`)
}
