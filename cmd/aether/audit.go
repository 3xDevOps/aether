package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:    "audit",
		short:   "show audit history: connector and assistant writes, connector, account-share and role changes",
		usage:   "aether audit [--connector <connector-id>] [--limit <n>] [--before <seq>]",
		example: "aether audit --connector con-7k2m9q4xbd",
		flags:   true,
		run:     runAudit,
	})
}

func runAudit(args []string) error {
	fs := newFlagSet("audit")
	connector := fs.String("connector", "", "only this connector's entries")
	limit := fs.Int("limit", protocol.AuditListDefaultLimit, "entries to show, at most 200")
	before := fs.Int64("before", 0, "only entries older than this seq, as the last page printed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: aether audit [--connector <connector-id>] [--limit <n>] [--before <seq>]")
	}
	return withControl(func(c *protocol.Client) error {
		var res protocol.AuditListResult
		if err := c.Call(protocol.MethodAuditList, protocol.AuditListParams{ConnectorID: *connector, Limit: *limit, Before: *before}, &res); err != nil {
			return olderServer(err)
		}
		return printAudit(os.Stdout, res, *connector)
	})
}

func printAudit(w io.Writer, res protocol.AuditListResult, connector string) error {
	if len(res.Entries) == 0 {
		_, err := fmt.Fprintln(w, "no audit entries")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SEQ\tTIME\tMEMBER\tVIA\tMETHOD\tTARGET\tOUTCOME")
	for _, e := range res.Entries {
		at, err := time.Parse(time.RFC3339, e.At)
		if err != nil {
			return fmt.Errorf("audit entry %d: at: %w", e.Seq, err)
		}
		via := e.Via
		if e.ConnectorID != "" {
			via += " " + e.Client + " " + e.ConnectorID
		}
		outcome := e.Outcome
		if e.Code != 0 {
			outcome += " " + strconv.Itoa(e.Code)
		}
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", e.Seq, at.Local().Format(time.DateTime), e.MemberID, via, e.Method, e.Target, outcome)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if res.NextBefore != 0 {
		filter := ""
		if connector != "" {
			filter = " --connector " + connector
		}
		_, _ = fmt.Fprintf(w, "Older: aether audit%s --before %d\n", filter, res.NextBefore)
	}
	return nil
}
