//go:build windows

package scheduler

import (
	"encoding/csv"
	"fmt"
	"hash/fnv"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const backendName = "Task Scheduler"

// sanitize maps an account name to a Task Scheduler-safe token (task names
// can't contain characters like ':' or '\\'). e.g. "drive:" → "drive".
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "default"
	}
	return out
}

// taskPrefix scopes every task RCSS manages for one account, e.g. "RCSS-drive-".
// Enumerating by this prefix is how apply and current find the account's tasks
// without touching anything else in Task Scheduler.
func taskPrefix(account string) string {
	return "RCSS-" + sanitize(account) + "-"
}

// taskName is the Task Scheduler task name for a job, e.g. "RCSS-drive-Upload"
// for an all-folders upload and "RCSS-drive-Upload-alpha-1f2e" for one limited
// to a folder. The folder's base name keeps the task readable and a short hash
// of the full path disambiguates two sources that share a base name.
func taskName(account string, j Job) string {
	name := taskPrefix(account) + j.Kind.Title()
	if j.Kind == Upload && j.Folder != "" {
		name += "-" + sanitize(filepath.Base(j.Folder)) + "-" + shortHash(j.Folder)
	}
	return name
}

// shortHash returns a 4-hex-digit digest of s, used to keep task names unique.
func shortHash(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%04x", h.Sum32()&0xffff)
}

// listTasks returns the names of every scheduled task whose name starts with
// prefix. It reads the locale-independent CSV listing and tolerates the absence
// of any task (schtasks exits non-zero when the query matches nothing).
func listTasks(prefix string) []string {
	out, err := exec.Command("schtasks", "/Query", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return nil
	}
	text := strings.ReplaceAll(string(out), "\x00", "") // the output may be UTF-16
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil
	}
	var names []string
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		// Task names are printed with their folder path, e.g. "\RCSS-drive-Upload".
		name := strings.TrimPrefix(strings.TrimSpace(row[0]), "\\")
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	return names
}

// apply removes every RCSS task belonging to the account (best effort) then
// creates the requested ones. Enumerating by prefix — rather than deleting a
// fixed pair of names — is what lets an account own any number of per-folder
// upload tasks. Tasks of other accounts are never listed, so they survive.
// Per-user tasks created this way do not require administrator rights. The
// headless rcss run writes its own per-account log, so no stdout redirection is
// needed here (logPath is unused on Windows).
func apply(account string, jobs []Job, exe, _ string) error {
	for _, name := range listTasks(taskPrefix(account)) {
		_ = deleteTask(name) // ignore "task not found"
	}
	for _, j := range jobs {
		if err := createTask(account, j, exe); err != nil {
			return err
		}
	}
	return nil
}

// createTask registers one job for the account via schtasks /Create.
func createTask(account string, j Job, exe string) error {
	// /TR is a single string; quote the executable so paths with spaces work,
	// and pass --account so the headless run targets the right account.
	tr := fmt.Sprintf(`"%s" %s --account "%s"`, exe, j.Kind.Arg(), account)
	if j.Kind == Upload && j.Folder != "" {
		tr += fmt.Sprintf(` --folder "%s"`, j.Folder)
	}
	args := []string{
		"/Create", "/F",
		"/TN", taskName(account, j),
		"/TR", tr,
		"/ST", fmt.Sprintf("%02d:%02d", j.Hour, j.Min),
	}
	if j.Weekly {
		args = append(args, "/SC", "WEEKLY", "/D", schtasksDay(j.Weekday))
	} else {
		args = append(args, "/SC", "DAILY")
	}
	if out, err := exec.Command("schtasks", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("creating task %s: %w: %s", taskName(account, j), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// schtasksDay maps a weekday to the /D token schtasks expects (SUN..SAT).
func schtasksDay(d time.Weekday) string {
	days := [...]string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}
	return days[(int(d)%7+7)%7]
}

// deleteTask removes a task; the error (e.g. when absent) is the caller's to
// ignore.
func deleteTask(name string) error {
	return exec.Command("schtasks", "/Delete", "/F", "/TN", name).Run()
}

// current enumerates the account's managed tasks and reconstructs the jobs from
// each task's XML, which (unlike the table/CSV output) is locale-independent.
func current(account string) ([]Job, error) {
	var jobs []Job
	for _, name := range listTasks(taskPrefix(account)) {
		out, err := exec.Command("schtasks", "/Query", "/TN", name, "/XML", "ONE").Output()
		if err != nil {
			continue // task disappeared between listing and query
		}
		jobs = append(jobs, parseTaskXML(string(out)))
	}
	return jobs, nil
}

// parseTaskXML extracts the kind, target folder, time and cadence from schtasks
// /XML output. The output may be UTF-16, so interleaved NUL bytes are stripped
// before scanning for the ASCII tags.
func parseTaskXML(xml string) Job {
	xml = strings.ReplaceAll(xml, "\x00", "")
	j := Job{Kind: Upload, Hour: -1, Min: -1, Weekly: strings.Contains(xml, "ScheduleByWeek")}
	args := splitArgs(unescapeXML(tagValue(xml, "Arguments")), "")
	for _, tok := range args {
		if tok == "clean" {
			j.Kind = Clean
			break
		}
		if tok == "upload" {
			break
		}
	}
	if j.Kind == Upload {
		j.Folder = flagValue(args, "--folder")
	}
	if j.Weekly {
		j.Weekday = parseDaysOfWeek(xml)
	}

	ts := tagValue(xml, "StartBoundary") // e.g. 2024-01-01T03:00:00
	if ts == "" {
		return j
	}
	t := strings.IndexByte(ts, 'T')
	if t < 0 || len(ts) < t+6 {
		return j
	}
	hhmm := strings.Split(ts[t+1:t+6], ":")
	if len(hhmm) != 2 {
		return j
	}
	if h, err := strconv.Atoi(hhmm[0]); err == nil {
		j.Hour = h
	}
	if m, err := strconv.Atoi(hhmm[1]); err == nil {
		j.Min = m
	}
	return j
}

// tagValue returns the text inside the first <tag>…</tag> pair, or "".
func tagValue(xml, tag string) string {
	open, close := "<"+tag+">", "</"+tag+">"
	i := strings.Index(xml, open)
	if i < 0 {
		return ""
	}
	rest := xml[i+len(open):]
	e := strings.Index(rest, close)
	if e < 0 {
		return ""
	}
	return rest[:e]
}

// unescapeXML undoes the entity escaping schtasks applies to the task arguments,
// so the quoted --account/--folder values can be tokenized by splitArgs.
func unescapeXML(s string) string {
	r := strings.NewReplacer("&quot;", `"`, "&apos;", "'", "&lt;", "<", "&gt;", ">", "&amp;", "&")
	return r.Replace(s)
}

// parseDaysOfWeek finds the weekday inside a weekly task's <DaysOfWeek> block
// (e.g. <DaysOfWeek><Wednesday/></DaysOfWeek>). Defaults to Sunday when absent.
func parseDaysOfWeek(xml string) time.Weekday {
	days := [...]struct {
		tag string
		day time.Weekday
	}{
		{"<Sunday", time.Sunday}, {"<Monday", time.Monday}, {"<Tuesday", time.Tuesday},
		{"<Wednesday", time.Wednesday}, {"<Thursday", time.Thursday},
		{"<Friday", time.Friday}, {"<Saturday", time.Saturday},
	}
	for _, d := range days {
		if strings.Contains(xml, d.tag) {
			return d.day
		}
	}
	return time.Sunday
}
