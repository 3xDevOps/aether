package main

import (
	"fmt"
	"os"
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
		return deviceChange(protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{Code: args[1]}, "approved")
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
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tMEMBER\tKIND\tLABEL\tSTATUS\tKEY\tLAST SEEN")
		for _, d := range res.Devices {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				d.ID, d.MemberID, d.Kind, d.Label, d.Status, d.Fingerprint, d.LastSeenAt)
		}
		return tw.Flush()
	})
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
