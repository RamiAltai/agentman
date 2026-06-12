package main

import (
	"context"
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
		os.Exit(1)
	}
	cmd := os.Args[1]
	rest := os.Args[2:]

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
	switch cmd {
	case "init":
		cmdInit(a)
		return
	case "whoami":
		cmdWhoami()
		return
	case "version", "--version", "-v":
		cmdVersion()
		return
	case "update", "upgrade":
		cmdUpdate(a)
		return
	case "db":
		cmdDB(a)
		return
	}

	c := NewClient()
	switch cmd {
	case "ls", "list":
		cmdLs(c, a)
	case "show":
		cmdShow(c, a)
	case "new":
		cmdNew(c, a)
	case "claim":
		cmdClaim(c, a)
	case "next":
		cmdNext(c, a)
	case "wait":
		cmdWait(c, a)
	case "status":
		cmdStatus(c, a)
	case "assign":
		cmdAssign(c, a)
	case "note", "comment":
		cmdNote(c, a)
	case "edit":
		cmdEdit(c, a)
	case "drop":
		cmdDrop(c, a)
	case "rm":
		cmdRm(c, a)
	case "projects":
		cmdProjects(c, a)
	case "project":
		cmdProject(c, a)
	case "categories":
		cmdCategories(c, a)
	case "category":
		cmdCategory(c, a)
	case "dep":
		cmdDep(c, a)
	case "help", "-h", "--help":
		usage()
	default:
		fail(1, "unknown command: %s (try `am help`)", cmd)
	}
}

func runServe(argv []string) {
	a := parse(argv)
	port := envOr("AGENTMAN_PORT", "8787")
	if v := a.flag("port"); v != "" {
		port = v
	}
	dbPath := a.flag("db")
	if dbPath == "" {
		dbPath = envOr("AGENTMAN_DB", defaultDBPath())
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		fail(1, "agentman: %v", err)
	}
	store, err := OpenStore(dbPath)
	if err != nil {
		fail(1, "agentman: open db: %v", err)
	}

	srv := NewServer(store)
	if a.has("log") || envOr("AGENTMAN_LOG", "") != "" {
		srv.logRequests = true
		log.Printf("agentman: request logging enabled")
	}
	baseCtx, cancelBase := context.WithCancel(context.Background())
	httpServer := &http.Server{
		Addr:              listenAddr(port),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	checkForUpdate() // non-blocking; logs once if a newer version is published

	go func() {
		log.Printf("agentman: dashboard on http://%s   (db: %s)", httpServer.Addr, dbPath)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("agentman: serve: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("agentman: shutting down")
	cancelBase() // unblock long-lived SSE handlers
	shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	httpServer.Shutdown(shutCtx)
	store.Close()
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

  am serve [--port 8787] [--db PATH] [--log]   run the dashboard + API

  am init <tasktype>                     set this session's identity (e.g. bugfix_050626_4821)
  am whoami                              print the current identity

  am ls [--mine] [--status S] [-p P] [-c CAT] [--all] [--ready] [--blocked] [--stale DUR] [--grep TEXT] [--label L]   list tasks (hides done)
  am show <id> [-c]                            task detail (+comments +deps)
  am new "title" [--body B] [-p P] [--priority N]   create, prints id
  am claim <id> [--steal-stale DUR]           assign me + ->doing (atomic; DUR is Go syntax, e.g. 30m, 48h)
  am next [-p P] [-c CAT]                     atomically pick + claim the best ready task (prints id; exit 3 if none)
  am wait <id> --done | am wait --ready [-p P] [-c CAT] [--timeout D]   block until done / a ready task exists (exit 7 on timeout)
  am status <id...> <todo|doing|blocked|done> change status (multiple ids allowed)
  am assign <id...> <agent|me|->              reassign ("-" = unassign; multiple ids allowed)
  am note <id> "text"                         add a comment
  am edit <id> [--title T] [--body B] [--priority N]
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
  am version                                  print version
  am update [version]                         reinstall the latest (or a given) version
  am db export [path] [--db PATH]                            export a DB snapshot (prints path)
  am db import <path> [--db PATH] [--yes]                    import a snapshot (stop serve first)
  am db prune [--db PATH] (--before YYYY-MM-DD | --keep N) [--yes]  delete old events rows

Identity: run 'am init <tasktype>' once per session (or set AGENTMAN_AGENT).
Env: AGENTMAN_URL (default http://127.0.0.1:8787), AGENTMAN_PROJECT (default project),
     AGENTMAN_CATEGORY (default category scope for ls/next/wait/project new).
     Add --json to any read to parse output.
Exit codes: 0 ok · 3 not found · 4 already claimed · 5 invalid · 6 server down · 7 timed out.
`)
}
