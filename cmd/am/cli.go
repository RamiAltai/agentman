package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Boolean (valueless) flags; everything else with a leading dash consumes the
// next token as its value. Aliases are canonicalised in canonFlag.
var boolFlags = map[string]bool{"json": true, "mine": true, "all": true, "comments": true}

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
		return "comments"
	case "a":
		return "assign"
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

// ---------- verbs ----------

func cmdLs(c *Client, a Args) {
	qs := url.Values{}
	proj := projectFor(a)
	if proj != "" && !a.has("all") {
		qs.Set("project", proj)
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
	fmt.Printf("created %s · %d comment%s\n", shortTime(t.CreatedAt), t.NComments, plural(t.NComments))
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
	st, data := c.do("POST", "/api/tasks/"+id+"/claim", nil)
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
			Assignee string `json:"assignee"`
		}
		json.Unmarshal(data, &e)
		fail(4, "claim: #%s held by %s", id, e.Assignee)
	default:
		fail(1, "claim: %s", apiErr(data, "error"))
	}
}

func cmdStatus(c *Client, a Args) {
	id := needID(a, "status")
	st := a.at(1)
	if !validStatus[st] {
		fail(5, "status must be one of: todo doing blocked done")
	}
	c.doOrFail("PATCH", "/api/tasks/"+id, map[string]any{"status": st})
}

func cmdAssign(c *Client, a Args) {
	id := needID(a, "assign")
	who := a.at(1)
	if who == "" {
		fail(5, "usage: am assign <id> <agent|me|->")
	}
	c.doOrFail("PATCH", "/api/tasks/"+id, map[string]any{"assignee": resolveWho(who)})
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

func cmdProjects(c *Client, a Args) {
	data := c.doOrFail("GET", "/api/projects", nil)
	var ps []Project
	json.Unmarshal(data, &ps)
	if a.has("json") {
		printJSON(ps)
		return
	}
	for _, p := range ps {
		fmt.Printf("%-10s %-20s todo:%d doing:%d blocked:%d done:%d\n",
			p.Slug, trunc(p.Name, 20), p.Counts["todo"], p.Counts["doing"], p.Counts["blocked"], p.Counts["done"])
	}
}

func cmdProject(c *Client, a Args) {
	if a.at(0) != "new" || a.at(1) == "" {
		fail(1, "usage: am project new <slug> [name]")
	}
	slug := a.at(1)
	name := slug
	if a.at(2) != "" {
		name = a.at(2)
	}
	data := c.doOrFail("POST", "/api/projects", map[string]any{"slug": slug, "name": name})
	var p Project
	json.Unmarshal(data, &p)
	fmt.Println(p.Slug)
}

// ---------- formatting helpers ----------

func taskLine(t Task, showProj bool) string {
	line := fmt.Sprintf("%-4d %-5s %-36s %s", t.ID, statusShort(t.Status), trunc(t.Title, 36), assignee(t.Assignee))
	if t.NComments > 0 {
		line += fmt.Sprintf(" (%dc)", t.NComments)
	}
	if showProj {
		line += " " + t.Project
	}
	return line
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
	os.Exit(code)
}
