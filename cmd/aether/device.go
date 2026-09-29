package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "device",
		short: "list, approve, or revoke the devices members reach the server through an edge with",
		run:   runDevice,
	})
}

func runDevice(args []string) error {
	const usage = "usage: aether device <list|approve <code>|revoke <device-id>>"
	if len(args) < 1 {
		return fmt.Errorf("%s", usage)
	}
	switch {
	case args[0] == "list" && len(args) == 1:
		return deviceList()
	case args[0] == "approve" && len(args) == 2:
		return deviceApprove(args[1], os.Stdin, os.Stdout)
	case args[0] == "revoke" && len(args) == 2:
		return deviceChange(protocol.MethodMemberDeviceRevoke, protocol.MemberDeviceRevokeParams{DeviceID: args[1]}, "revoked")
	}
	return fmt.Errorf("%s", usage)
}

// deviceList prints your devices, or every member's for an admin.
func deviceList() error {
	return withControl(func(c *protocol.Client) error {
		var res protocol.MemberDeviceListResult
		if err := c.Call(protocol.MethodMemberDeviceList, struct{}{}, &res); err != nil {
			return err
		}
		return printDevices(res.Devices)
	})
}

// printDevices lists devices with the edge account each signed in as. A
// device waiting on an invitation has no member yet: it names the
// invitation instead.
func printDevices(devices []protocol.Device) error {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tMEMBER\tACCOUNT\tLABEL\tSTATUS\tKEY\tLAST SEEN")
	for _, d := range devices {
		member := d.MemberID
		if d.InvitationID != "" {
			member = "invitation " + d.InvitationID
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s %s\t%s\t%s\t%s\t%s\n",
			d.ID, member, d.Provider, d.Account, d.Label, d.Status, d.Fingerprint, d.LastSeenAt)
	}
	return tw.Flush()
}

func deviceChange(method string, params any, verb string) error {
	return withControl(func(c *protocol.Client) error {
		var res protocol.MemberDeviceResult
		if err := c.Call(method, params, &res); err != nil {
			return err
		}
		fmt.Printf("%s device %s %q of member %s\n", verb, res.Device.ID, res.Device.Label, res.Device.MemberID)
		return nil
	})
}

// deviceApprove shows the device code names and whom approving admits it
// as, and approves that device once the person answers yes.
func deviceApprove(code string, in io.Reader, out io.Writer) error {
	return withControl(func(c *protocol.Client) error {
		var found protocol.MemberDeviceLookupResult
		if err := c.Call(protocol.MethodMemberDeviceLookup, protocol.MemberDeviceLookupParams{Code: code}, &found); err != nil {
			return err
		}
		ok, err := confirmDeviceApproval(in, out, found)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("device approve: not approved")
		}
		var res protocol.MemberDeviceResult
		params := protocol.MemberDeviceApproveParams{Code: code, DeviceID: found.Device.ID}
		if err := c.Call(protocol.MethodMemberDeviceApprove, params, &res); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "approved device %s %q of member %s\n", res.Device.ID, res.Device.Label, res.Device.MemberID)
		return nil
	})
}

// confirmDeviceApproval prints what approving found admits and reads one
// line from in; only "y" or "yes" (either case) approves. The member
// follows from the account the device signed in as, which the edge
// vouches for, so a code someone hands over can name your own member.
func confirmDeviceApproval(in io.Reader, out io.Writer, found protocol.MemberDeviceLookupResult) (bool, error) {
	d := found.Device
	_, _ = fmt.Fprintf(out, "device %q, key %s\n  signed in as: %s account %s\n", d.Label, d.Fingerprint, d.Provider, d.Account)
	if found.MemberID != "" {
		_, _ = fmt.Fprintf(out, "  admits it as: %s (%s), %s\n", found.DisplayName, found.MemberID, found.Role)
	} else {
		_, _ = fmt.Fprintf(out, "  admits it as: a new member, %s\n", found.Role)
	}
	if d.InvitationID != "" {
		_, _ = fmt.Fprintf(out, "  accepts invitation %s\n", d.InvitationID)
	}
	_, _ = fmt.Fprint(out, "Whoever holds this device gets that member's access. approve it? [y/N]: ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes", nil
}
