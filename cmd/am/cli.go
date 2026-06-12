package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// osExit is the process-exit function. Tests replace it with a panic-based
// stub so that fail() can be tested without killing the test binary.
var osExit = os.Exit

// Boolean (valueless) flags; everything else with a leading dash consumes the
// next token as its value. Aliases are canonicalised in canonFlag.
// Registration is global: adding "done" (for `am wait <id> --done`) makes
// --done valueless for EVERY verb — fine today, since no verb takes a done
// value flag.
var boolFlags = map[string]bool{"json": true, "mine": true, "all": true, "comments": true, "yes": true, "log": true, "ready": true, "blocked": true, "done": true}

type Args struct {
	pos   []string
	flags map[string]string
	bools map[string]bool
}

func (a Args) flag(k string) string { return a.flags[k] }
func (a Args) has(k string) bool    { return a.bools[k] }
func (a Args) at(i int) string {
	if i < len(a.pos) {
		return a.pos[i]
	}
	return ""
}

// parse splits argv into positionals, value flags, and boolean flags in any
// order, so `am new "title" --body x` and `am new --body x "title"` both work.
func parse(argv []string) Args {
	a := Args{flags: map[string]string{}, bools: map[string]bool{}}
	for i := 0; i < len(argv); i++ {
		s := argv[i]
		if len(s) > 1 && s[0] == '-' && s != "-" {
			key := canonFlag(strings.TrimLeft(s, "-"))
			if eq := strings.IndexByte(key, '='); eq >= 0 {
				a.flags[canonFlag(key[:eq])] = key[eq+1:]
				continue
			}
			if boolFlags[key] {
				a.bools[key] = true
				continue
			}
			if i+1 < len(argv) {
				a.flags[key] = argv[i+1]
				i++
			}
			continue
		}
		a.pos = append(a.pos, s)
	}
	return a
}

func canonFlag(k string) string {
	switch k {
	case "p":
		return "project"
	case "s":
		return "status"
	case "c":
		// -c is the category flag everywhere; main.go rewrites -c → --comments
		// for `am show` only, preserving the documented `am show <id> -c`.
		return "category"
	case "a":
		return "assign"
	case "l":
		return "label"
	default:
		return k
	}
}

func me() string { return resolveAgent() }

// projectFor resolves the active project: -p/--project, else AGENTMAN_PROJECT.
func projectFor(a Args) string {
	if p := a.flag("project"); p != "" {
		return p
	}
	return os.Getenv("AGENTMAN_PROJECT")
}

// categoryFor resolves the active category: -c/--category, else AGENTMAN_CATEGORY.
func categoryFor(a Args) string {
	if c := a.flag("category"); c != "" {
		return c
	}
	return os.Getenv("AGENTMAN_CATEGORY")
}

// ---------- verbs ----------

func cmdLs(c *Client, a Args) {
	qs := url.Values{}
	proj := projectFor(a)
	if proj != "" && !a.has("all") {
		qs.Set("project", proj)
	}
	if cat := categoryFor(a); cat != "" && !a.has("all") {
		qs.Set("category", cat)
	}
	switch {
	case a.flag("status") != "":
		qs.Set("status", a.flag("status"))
	case !a.has("all"):
		qs.Set("status", "todo,doing,blocked") // hide done by default
	}
	if a.has("mine") {
		if me() == "" {
			fail(5, "set AGENTMAN_AGENT to use --mine")
		}
		qs.Set("assignee", me())
	}
	if a.has("ready") {
		qs.Set("ready", "true")
	}
	if a.has("blocked") {
		qs.Set("blocked", "true")
	}
	if v := a.flag("stale"); v != "" {
		qs.Set("stale", v) // Go duration, e.g. 30m / 48h; server validates
	}
	if v := a.flag("grep"); v != "" {
		qs.Set("q", v) // substring match on title OR body (ASCII-case-insensitive)
	}
	if v := a.flag("label"); v != "" {
		qs.Set("label", v) // server validates/normalizes
	}
	qs.Set("limit", "50")

	data := c.doOrFail("GET", "/api/tasks?"+qs.Encode(), nil)
	var tasks []Task
	json.Unmarshal(data, &tasks)
	if a.has("json") {
		printJSON(tasks)
		return
	}
	showProj := proj == "" || a.has("all")
	for _, t := range tasks {
		fmt.Println(taskLine(t, showProj))
	}
	if len(tasks) >= 50 {
		fmt.Println("… 50 shown; narrow with --status or -p")
	}
}

func cmdShow(c *Client, a Args) {
	id := needID(a, "show")
	data := c.doOrFail("GET", "/api/tasks/"+id, nil)
	var t Task
	json.Unmarshal(data, &t)
	if a.has("json") {
		printJSON(t)
		return
	}
	fmt.Printf("#%d %s  %s  [%s] p%d\n", t.ID, t.Status, assignee(t.Assignee), t.Project, t.Priority)
	fmt.Println(t.Title)
	if strings.TrimSpace(t.Body) != "" {
		fmt.Println(t.Body)
	}
	if len(t.Labels) > 0 {
		fmt.Println("labels: " + strings.Join(t.Labels, " "))
	}
	fmt.Printf("created %s · %d comment%s\n", shortTime(t.CreatedAt), t.NComments, plural(t.NComments))
	if len(t.DependsOn) > 0 {
		parts := make([]string, len(t.DependsOn))
		for i, d := range t.DependsOn {
			parts[i] = fmt.Sprintf("%s-%d %s", d.Project, d.Ref, d.Status)
		}
		fmt.Println("depends on: " + strings.Join(parts, " · "))
	}
	if len(t.Blocks) > 0 {
		parts := make([]string, len(t.Blocks))
		for i, d := range t.Blocks {
			parts[i] = fmt.Sprintf("%s-%d", d.Project, d.Ref)
		}
		fmt.Println("blocks: " + strings.Join(parts, " · "))
	}
	if a.has("comments") {
		for _, cm := range t.Comments {
			fmt.Printf("%s %s  %s\n", cm.Author, shortTime(cm.CreatedAt), cm.Body)
		}
	}
}

func cmdNew(c *Client, a Args) {
	title := a.at(0)
	if strings.TrimSpace(title) == "" {
		fail(5, "usage: am new \"title\" [--body ..] [-p project] [--priority N]")
	}
	proj := projectFor(a)
	if proj == "" {
		fail(5, "no project: set AGENTMAN_PROJECT or pass -p <slug>")
	}
	body := map[string]any{"project": proj, "title": title}
	if v := a.flag("body"); v != "" {
		body["body"] = v
	}
	if v := a.flag("priority"); v != "" {
		body["priority"] = atoiOr(v, 2)
	}
	if v := a.flag("assign"); v != "" {
		body["assignee"] = resolveWho(v)
	}
	data := c.doOrFail("POST", "/api/tasks", body)
	var t Task
	json.Unmarshal(data, &t)
	fmt.Println(t.ID) // print only the id
}

func cmdClaim(c *Client, a Args) {
	id := needID(a, "claim")
	if me() == "" {
		fail(5, "set AGENTMAN_AGENT to claim tasks")
	}
	var body any
	if v := a.flag("steal-stale"); v != "" {
		body = map[string]any{"steal_stale": v} // Go duration, e.g. 30m / 48h
	}
	st, data := c.do("POST", "/api/tasks/"+id+"/claim", body)
	switch {
	case st == 0:
		fail(6, "agentman: cannot reach server (is `am serve` running?)")
	case st >= 200 && st < 300:
		var t Task
		json.Unmarshal(data, &t)
		fmt.Println(t.ID)
	case st == 404:
		fail(3, "claim: #%s not found", id)
	case st == 409:
		var e struct {
			Error       string  `json:"error"`
			Assignee    string  `json:"assignee"`
			OpenPrereqs []int64 `json:"open_prereqs"`
		}
		json.Unmarshal(data, &e)
		if e.Error == "blocked" {
			// Format the open prereq IDs for a clear error message.
			parts := make([]string, len(e.OpenPrereqs))
			for i, id := range e.OpenPrereqs {
				parts[i] = fmt.Sprintf("#%d", id)
			}
			fail(4, "claim: #%s blocked — prereqs not done (%s)", id, strings.Join(parts, " "))
		}
		if e.Error == "not_stale" {
			fail(4, "claim: #%s held by %s (not stale yet)", id, e.Assignee)
		}
		fail(4, "claim: #%s held by %s", id, e.Assignee)
	case st == 400:
		fail(5, "claim: %s", apiErr(data, "invalid request"))
	default:
		fail(1, "claim: %s", apiErr(data, "error"))
	}
}

// cmdNext atomically picks and claims the highest-priority ready task
// (FIFO within a priority). Prints only the claimed id, like cmdClaim.
func cmdNext(c *Client, a Args) {
	if me() == "" {
		fail(5, "set AGENTMAN_AGENT to pick up tasks")
	}
	scope := map[string]any{}
	if proj := projectFor(a); proj != "" {
		scope["project"] = proj
	}
	if cat := categoryFor(a); cat != "" {
		scope["category"] = cat
	}
	var body any
	if len(scope) > 0 {
		body = scope
	}
	st, data := c.do("POST", "/api/tasks/next", body)
	switch {
	case st == 0:
		fail(6, "agentman: cannot reach server (is `am serve` running?)")
	case st >= 200 && st < 300:
		var t Task
		json.Unmarshal(data, &t)
		if a.has("json") {
			printJSON(t)
			return
		}
		fmt.Println(t.ID)
	case st == 404:
		// Also covers a bad -p/-c slug (the server can't tell them apart).
		fail(3, "next: no ready task")
	case st == 400:
		fail(5, "next: %s", apiErr(data, "invalid request"))
	default:
		fail(1, "next: %s", apiErr(data, "error"))
	}
}

// cmdStatus sets the status of one or more tasks: am status <id...> <status>.
func cmdStatus(c *Client, a Args) {
	if len(a.pos) < 2 || !validStatus[a.at(len(a.pos)-1)] {
		fail(5, "usage: am status <id...> <todo|doing|blocked|done>")
	}
	st := a.at(len(a.pos) - 1)
	bulkPatch(c, "status", a.pos[:len(a.pos)-1], map[string]any{"status": st})
}

// cmdAssign reassigns one or more tasks: am assign <id...> <agent|me|->.
func cmdAssign(c *Client, a Args) {
	if len(a.pos) < 2 {
		fail(5, "usage: am assign <id...> <agent|me|->")
	}
	who := a.at(len(a.pos) - 1)
	bulkPatch(c, "assign", a.pos[:len(a.pos)-1], map[string]any{"assignee": resolveWho(who)})
}

// bulkPatch PATCHes each id in turn — a client-side loop, so each task gets
// its own event (the dashboard feed stays per-task). Server down aborts
// immediately (exit 6); any other failure prints one stderr line per id and
// CONTINUES; the final exit code is the FIRST failure's mapping (0 if none).
func bulkPatch(c *Client, verb string, ids []string, patch map[string]any) {
	firstFail := 0
	for _, id := range ids {
		st, data := c.do("PATCH", "/api/tasks/"+id, patch)
		if st == 0 {
			fail(6, "agentman: cannot reach server at %s (is `am serve` running?)", c.base)
		}
		if code := exitCodeFor(st); code != 0 {
			fmt.Fprintf(os.Stderr, "%s: #%s %s\n", verb, id, apiErr(data, "error "+strconv.Itoa(st)))
			if firstFail == 0 {
				firstFail = code
			}
		}
	}
	if firstFail != 0 {
		osExit(firstFail)
	}
}

func cmdNote(c *Client, a Args) {
	id := needID(a, "note")
	body := a.at(1)
	if strings.TrimSpace(body) == "" {
		fail(5, "usage: am note <id> \"text\"")
	}
	c.doOrFail("POST", "/api/tasks/"+id+"/comments", map[string]any{"body": body})
}

func cmdEdit(c *Client, a Args) {
	id := needID(a, "edit")
	patch := map[string]any{}
	if v := a.flag("title"); v != "" {
		patch["title"] = v
	}
	if v, ok := a.flags["body"]; ok {
		patch["body"] = v
	}
	if v := a.flag("priority"); v != "" {
		patch["priority"] = atoiOr(v, 2)
	}
	if len(patch) == 0 {
		fail(1, "edit: nothing to change (use --title/--body/--priority)")
	}
	c.doOrFail("PATCH", "/api/tasks/"+id, patch)
}

func cmdDrop(c *Client, a Args) {
	id := needID(a, "drop")
	c.doOrFail("PATCH", "/api/tasks/"+id, map[string]any{"assignee": "", "status": "todo"})
}

// cmdRm hard-deletes a task. Silent success (scriptable for agents).
func cmdRm(c *Client, a Args) {
	id := needID(a, "rm")
	c.doOrFail("DELETE", "/api/tasks/"+id, nil)
}

func cmdProjects(c *Client, a Args) {
	path := "/api/projects"
	if a.has("all") {
		path += "?archived=true"
	}
	data := c.doOrFail("GET", path, nil)
	var ps []Project
	json.Unmarshal(data, &ps)
	if a.has("json") {
		printJSON(ps)
		return
	}
	for _, p := range ps {
		archived := ""
		if p.ArchivedAt != "" {
			archived = " (archived)"
		}
		fmt.Printf("%-10s %-20s todo:%d doing:%d blocked:%d done:%d%s\n",
			p.Slug, trunc(p.Name, 20), p.Counts["todo"], p.Counts["doing"], p.Counts["blocked"], p.Counts["done"], archived)
	}
}

func cmdProject(c *Client, a Args) {
	switch a.at(0) {
	case "new":
		if a.at(1) == "" {
			fail(1, "usage: am project new <slug> [name] -c <category>")
		}
		cat := categoryFor(a)
		if cat == "" {
			fail(5, "no category: pass -c <slug> or set AGENTMAN_CATEGORY")
		}
		slug := a.at(1)
		name := slug
		if a.at(2) != "" {
			name = a.at(2)
		}
		data := c.doOrFail("POST", "/api/projects", map[string]any{"slug": slug, "name": name, "category": cat})
		var p Project
		json.Unmarshal(data, &p)
		fmt.Println(p.Slug)
	case "edit":
		slug := a.at(1)
		if slug == "" {
			fail(1, "usage: am project edit <slug> [--slug NEW] [--name N] [--vault-id X] [--vault-path Y]")
		}
		patch := map[string]any{}
		if v := a.flag("slug"); v != "" {
			patch["slug"] = v
		}
		if v := a.flag("name"); v != "" {
			patch["name"] = v
		}
		// ok-form so empty values clear the vault binding (cmdEdit --body precedent).
		if v, ok := a.flags["vault-id"]; ok {
			patch["vault_project_id"] = v
		}
		if v, ok := a.flags["vault-path"]; ok {
			patch["vault_path"] = v
		}
		if len(patch) == 0 {
			fail(1, "project edit: nothing to change (use --slug/--name/--vault-id/--vault-path)")
		}
		c.doOrFail("PATCH", "/api/projects/"+slug, patch)
	case "archive":
		if a.at(1) == "" {
			fail(1, "usage: am project archive <slug>")
		}
		c.doOrFail("POST", "/api/projects/"+a.at(1)+"/archive", nil)
	case "unarchive":
		if a.at(1) == "" {
			fail(1, "usage: am project unarchive <slug>")
		}
		c.doOrFail("POST", "/api/projects/"+a.at(1)+"/unarchive", nil)
	case "rm":
		slug := a.at(1)
		if slug == "" {
			fail(1, "usage: am project rm <slug> --yes")
		}
		if !a.has("yes") {
			fail(1, "am project rm <slug> --yes  (deletes the project and ALL its tasks/comments)")
		}
		c.doOrFail("DELETE", "/api/projects/"+slug, nil)
	default:
		fail(1, "usage: am project <new|edit|archive|unarchive|rm> ...")
	}
}

func cmdCategories(c *Client, a Args) {
	path := "/api/categories"
	if a.has("all") {
		path += "?archived=true"
	}
	data := c.doOrFail("GET", path, nil)
	var cs []Category
	json.Unmarshal(data, &cs)
	if a.has("json") {
		printJSON(cs)
		return
	}
	for _, cat := range cs {
		archived := ""
		if cat.ArchivedAt != "" {
			archived = " (archived)"
		}
		fmt.Printf("%-10s %-20s%s\n", cat.Slug, trunc(cat.Name, 20), archived)
	}
}

func cmdCategory(c *Client, a Args) {
	switch a.at(0) {
	case "new":
		if a.at(1) == "" {
			fail(1, "usage: am category new <slug> [name]")
		}
		slug := a.at(1)
		name := slug
		if a.at(2) != "" {
			name = a.at(2)
		}
		data := c.doOrFail("POST", "/api/categories", map[string]any{"slug": slug, "name": name})
		var cat Category
		json.Unmarshal(data, &cat)
		fmt.Println(cat.Slug)
	case "archive":
		if a.at(1) == "" {
			fail(1, "usage: am category archive <slug>")
		}
		c.doOrFail("POST", "/api/categories/"+a.at(1)+"/archive", nil)
	case "unarchive":
		if a.at(1) == "" {
			fail(1, "usage: am category unarchive <slug>")
		}
		c.doOrFail("POST", "/api/categories/"+a.at(1)+"/unarchive", nil)
	default:
		fail(1, "usage: am category <new|archive|unarchive> ...")
	}
}

// ---------- formatting helpers ----------

func taskLine(t Task, showProj bool) string {
	line := fmt.Sprintf("%-4d %-5s %-36s %s", t.ID, statusShort(t.Status), trunc(t.Title, 36), assignee(t.Assignee))
	if t.NComments > 0 {
		line += fmt.Sprintf(" (%dc)", t.NComments)
	}
	if t.NOpenPrereqs > 0 {
		line += fmt.Sprintf(" [blk:%d]", t.NOpenPrereqs)
	} else if t.NPrereqs > 0 {
		line += " [ready]"
	}
	if showProj {
		line += " " + t.Project
	}
	return line
}

func cmdDep(c *Client, a Args) {
	sub := a.at(0)
	switch sub {
	case "add":
		id := a.at(1)
		if id == "" {
			fail(1, "usage: am dep add <id> <prereq> [prereq…]")
		}
		prereqs := a.pos[2:] // remaining positionals
		if len(prereqs) == 0 {
			fail(1, "usage: am dep add <id> <prereq> [prereq…]")
		}
		for _, prereq := range prereqs {
			c.doOrFail("POST", "/api/tasks/"+id+"/deps", map[string]any{"depends_on": prereq})
		}
	case "rm":
		id := a.at(1)
		prereq := a.at(2)
		if id == "" || prereq == "" {
			fail(1, "usage: am dep rm <id> <prereq>")
		}
		c.doOrFail("DELETE", "/api/tasks/"+id+"/deps/"+prereq, nil)
	default:
		fail(1, "usage: am dep <add|rm> ...")
	}
}

// cmdLabel adds/removes/prints task labels. It takes RAW argv (not parse()),
// because parse() would treat a removal token like "-bar" as a value flag and
// swallow the next token. Dispatched in main.go before the parse() call.
//   - `am label <id>` prints the task's labels, space-separated, on one line.
//   - `am label <id> +foo bar -baz` adds foo and bar, removes baz (silent success).
func cmdLabel(c *Client, argv []string) {
	const usage = "usage: am label <id> [+add ...] [-remove ...]"
	if len(argv) == 0 {
		fail(1, usage)
	}
	id := argv[0]
	if len(argv) == 1 {
		data := c.doOrFail("GET", "/api/tasks/"+id, nil)
		var t Task
		json.Unmarshal(data, &t)
		if len(t.Labels) > 0 {
			fmt.Println(strings.Join(t.Labels, " "))
		}
		return
	}
	for _, tok := range argv[1:] {
		if strings.HasPrefix(tok, "--") {
			fail(5, usage)
		}
		if tok == "-p" || tok == "-c" {
			fail(5, tok+" is a global flag, not a label removal; "+usage)
		}
		remove := strings.HasPrefix(tok, "-")
		l := strings.TrimPrefix(strings.TrimPrefix(tok, "-"), "+")
		if l == "" {
			fail(5, usage)
		}
		if remove {
			c.doOrFail("DELETE", "/api/tasks/"+id+"/labels/"+url.PathEscape(l), nil)
		} else {
			c.doOrFail("POST", "/api/tasks/"+id+"/labels", map[string]any{"label": l})
		}
	}
}

func statusShort(s string) string {
	if s == "blocked" {
		return "block"
	}
	return s
}

func assignee(s string) string {
	switch {
	case s == "":
		return "-"
	case s == me():
		return "@me"
	default:
		return "@" + s
	}
}

// resolveWho maps the CLI tokens "me" and "-" to a concrete assignee value.
func resolveWho(s string) string {
	switch s {
	case "me":
		if me() == "" {
			fail(5, "set AGENTMAN_AGENT to assign to me")
		}
		return me()
	case "-", "none":
		return ""
	default:
		return s
	}
}

func needID(a Args, verb string) string {
	if a.at(0) == "" {
		fail(1, "usage: am %s <id> ...", verb)
	}
	return a.at(0)
}

func trunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func shortTime(iso string) string {
	if len(iso) >= 16 {
		return strings.Replace(iso[:16], "T", " ", 1)
	}
	return iso
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func printJSON(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func fail(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	osExit(code)
}
