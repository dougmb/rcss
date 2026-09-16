//go:build !windows

package scheduler

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const backendName = "crontab"

const (
	beginMarker = "# >>> RCSS-managed >>>"
	endMarker   = "# <<< RCSS-managed <<<"
)

// readCrontab returns the user's current crontab, or "" if none exists.
func readCrontab() (string, error) {
	cmd := exec.Command("crontab", "-l")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// `crontab -l` exits non-zero when there is no crontab yet ("no crontab
		// for user"). Any other failure must not be mistaken for an empty
		// crontab: the caller rewrites the whole crontab from what it read.
		msg := strings.TrimSpace(stderr.String())
		lower := strings.ToLower(msg)
		if _, ok := err.(*exec.ExitError); ok &&
			(msg == "" || strings.Contains(lower, "no crontab") || strings.Contains(lower, "no such file")) {
			return "", nil
		}
		return "", fmt.Errorf("reading crontab: %w: %s", err, msg)
	}
	return string(out), nil
}

// writeCrontab installs content as the user's crontab via `crontab -`.
func writeCrontab(content string) error {
	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(content)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("writing crontab: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// stripBlock returns content with the RCSS-managed block (markers included)
// removed, leaving all other lines intact.
func stripBlock(content string) []string {
	var kept []string
	inBlock := false
	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.TrimSpace(line) == beginMarker:
			inBlock = true
		case strings.TrimSpace(line) == endMarker:
			inBlock = false
		case !inBlock:
			kept = append(kept, line)
		}
	}
	// Trim trailing empties left behind.
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	return kept
}

// managedLines returns the cron lines currently inside the RCSS block (without
// the markers), or nil if the block is absent.
func managedLines() ([]string, error) {
	content, err := readCrontab()
	if err != nil {
		return nil, err
	}
	var lines []string
	inBlock := false
	for _, line := range strings.Split(content, "\n") {
		switch strings.TrimSpace(line) {
		case beginMarker:
			inBlock = true
		case endMarker:
			inBlock = false
		default:
			if inBlock && strings.TrimSpace(line) != "" {
				lines = append(lines, line)
			}
		}
	}
	return lines, nil
}

// setManaged replaces the RCSS block with the given cron lines. Empty input
// removes the block entirely. Other crontab entries are preserved.
func setManaged(lines []string) error {
	content, err := readCrontab()
	if err != nil {
		return err
	}
	kept := stripBlock(content)

	out := strings.Join(kept, "\n")
	if len(lines) > 0 {
		block := append([]string{beginMarker}, lines...)
		block = append(block, endMarker)
		if out != "" {
			out += "\n"
		}
		out += strings.Join(block, "\n")
	}
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return writeCrontab(out)
}

// apply rewrites the managed block: it keeps every line that doesn't belong to
// account, then appends one line per job for this account. Each line runs the
// rcss binary headless with --account so the right account is selected, and
// appends stdout/stderr to logPath.
func apply(account string, jobs []Job, exe, logPath string) error {
	existing, err := managedLines()
	if err != nil {
		return err
	}
	var lines []string
	for _, ln := range existing {
		if lineAccount(ln) != account {
			lines = append(lines, ln) // preserve other accounts' jobs
		}
	}
	for _, j := range jobs {
		lines = append(lines, formatJobLine(account, j, exe, logPath))
	}
	return setManaged(lines)
}

// formatJobLine renders one managed crontab line for a job. Daily jobs use "*"
// for the day-of-week field; weekly jobs set it to the weekday number (0=Sun..
// 6=Sat). An upload limited to one folder carries it as --folder. Every path is
// double-quoted so folders, binaries and logs with spaces survive both the
// shell and the round-trip back through parseManagedLine.
//
// Only stderr is appended to logPath. The headless run already writes every log
// line to that same file through the Logger and merely echoes them to stdout for
// cron's mail; redirecting stdout there too would record each line twice.
// Keeping stderr means a failure before the Logger exists (a missing account, no
// rclone) is still captured.
func formatJobLine(account string, j Job, exe, logPath string) string {
	dow := "*"
	if j.Weekly {
		dow = strconv.Itoa(int(j.Weekday))
	}
	cmd := fmt.Sprintf("%s %s --account %s", cronQuote(exe), j.Kind.Arg(), cronQuote(account))
	if j.Kind == Upload && j.Folder != "" {
		cmd += " --folder " + cronQuote(j.Folder)
	}
	return fmt.Sprintf(`%d %d * * %s %s >/dev/null 2>>%s`, j.Min, j.Hour, dow, cmd, cronQuote(logPath))
}

// cronEscapes are the characters cronQuote backslash-escapes inside quotes.
const cronEscapes = "\\\"$`%"

// cronQuote double-quotes s for a crontab command. The shell would expand $ and
// ` inside double quotes and cron turns an unescaped % into a newline, so those
// are backslash-escaped along with \ and ". cron strips the backslash before a
// %, and the shell strips the others, so the command sees s unchanged.
func cronQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(cronEscapes, s[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('"')
	return b.String()
}

// current parses the managed crontab lines that belong to account back into
// jobs. Lines that don't match the expected shape are skipped.
func current(account string) ([]Job, error) {
	lines, err := managedLines()
	if err != nil {
		return nil, err
	}
	var jobs []Job
	for _, ln := range lines {
		if lineAccount(ln) != account {
			continue
		}
		if j, ok := parseManagedLine(ln); ok {
			jobs = append(jobs, j)
		}
	}
	return jobs, nil
}

// parseManagedLine parses one managed crontab line back into a Job. It reads the
// minute/hour fields, recovers the weekday from the day-of-week field (any value
// other than "*" means weekly; cron's 7 wraps to Sunday), detects the kind from
// the rcss subcommand token, and recovers the target folder from --folder.
// Returns ok=false for malformed lines.
func parseManagedLine(line string) (Job, bool) {
	f := splitArgs(line, cronEscapes)
	if len(f) < 6 {
		return Job{}, false
	}
	min, err1 := strconv.Atoi(f[0])
	hour, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil {
		return Job{}, false
	}
	j := Job{Kind: Upload, Hour: hour, Min: min}
	if dow := f[4]; dow != "*" {
		if n, err := strconv.Atoi(dow); err == nil {
			j.Weekly = true
			j.Weekday = time.Weekday(((n % 7) + 7) % 7) // 7 → Sunday
		}
	}
	for _, tok := range f[5:] {
		if tok == "clean" {
			j.Kind = Clean
			break
		}
		if tok == "upload" {
			break
		}
	}
	if j.Kind == Upload {
		j.Folder = flagValue(f, "--folder")
	}
	return j, true
}

// lineAccount returns the account a managed cron line targets (the value of
// --account), or "" if it carries none.
func lineAccount(line string) string {
	return flagValue(splitArgs(line, cronEscapes), "--account")
}
