package scheduler

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/collab"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestRoomImageReadFailureAllowsFreshKeyRetry(t *testing.T) {
	t.Parallel()
	for _, delayed := range []bool{false, true} {
		name := "immediate"
		if delayed {
			name = "delayed"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e, rt := newACPEnv(t)
			run := e.launchACP(t, "")
			data := schedulerPNG(t)
			reference, err := e.sched.SaveTerminalImage(t.Context(), e.member.ID, run.ID, ".png", data)
			if err != nil {
				t.Fatal(err)
			}
			home, err := e.cfg.Homes.Path(run.HomeMember())
			if err != nil {
				t.Fatal(err)
			}
			imagePath := filepath.Join(home, ".aether", "terminal-images", filepath.Base(reference))
			now := time.Now()
			ctl := control.New(control.Config{})
			admissions := 0
			room, err := collab.New(collab.Config{
				Store: e.db, Runs: e.db, Workspaces: e.db, Bus: e.bus, Control: ctl,
				Inject: e.sched.Inject, Now: func() time.Time { return now },
				Attachments: func(ctx context.Context, _ domain.WorkspaceID, id domain.RunID, ref string) error {
					if validationErr := e.sched.ValidateTerminalImage(ctx, id, ref); validationErr != nil {
						return validationErr
					}
					admissions++
					// The first immediate delivery loses its file only after real
					// admission succeeds. This is synchronous, not a timing race.
					if !delayed && admissions == 1 {
						return os.Remove(imagePath)
					}
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			input := collab.MessageInput{
				WorkspaceID: e.ws.ID, RunID: run.ID, ActorID: e.member.ID,
				Kind: store.RoomMessageSteerRequest, Attachments: []string{reference}, IdempotencyKey: "first-image",
			}
			if !delayed {
				lease, _, acquireErr := ctl.Acquire(string(run.ID), string(e.member.ID), "image-retry", false)
				if acquireErr != nil {
					t.Fatal(acquireErr)
				}
				input.ControllerSessionID, input.ControllerGeneration = lease.SessionID, lease.Generation
			}
			first, err := room.Post(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			if admissions != 1 {
				t.Fatalf("successful image admissions = %d, want 1", admissions)
			}
			deliverDue := func() {
				t.Helper()
				now = now.Add(collab.SteeringDelay + time.Second)
				count, deliveryErr := room.DeliverDue(t.Context(), 10)
				if deliveryErr != nil || count != 1 {
					t.Fatalf("DeliverDue = %d, %v; want one settled delivery", count, deliveryErr)
				}
			}
			if delayed {
				if first.Message.State != store.RoomMessageQueued || first.Receipt != "" {
					t.Fatalf("initial delayed message = %+v", first)
				}
				if removeErr := os.Remove(imagePath); removeErr != nil {
					t.Fatal(removeErr)
				}
				deliverDue()
			} else if first.Receipt != collab.ReceiptNotSent || first.Message.State != store.RoomMessageNotSent {
				t.Fatalf("initial delivery = %+v, want not_sent", first)
			}
			failed, err := e.db.GetRoomMessage(t.Context(), first.Message.ID)
			if err != nil || failed.State != store.RoomMessageNotSent {
				t.Fatalf("stored failed message = %+v, %v; want not_sent", failed, err)
			}
			// Check that the scheduler retains both the definite non-delivery
			// marker and the underlying filesystem cause.
			_, readErr := e.sched.Inject(t.Context(), run.ID, e.member.ID,
				domain.AgentPrompt{Attachments: []string{reference}, MessageID: first.Message.ID}, false, nil)
			if !errors.Is(readErr, domain.ErrPromptNotSent) || !errors.Is(readErr, os.ErrNotExist) || collab.ClassifyReceipt(readErr) != collab.ReceiptNotSent {
				t.Fatalf("missing image error lost delivery classification or cause: %v", readErr)
			}
			if prompts := rt.all()[0].wire.prompts(t); len(prompts) != 0 {
				t.Fatalf("failed image produced %d ACP prompts", len(prompts))
			}
			if restoreErr := os.WriteFile(imagePath, data, 0o600); restoreErr != nil {
				t.Fatal(restoreErr)
			}
			same, err := room.Post(t.Context(), input)
			if err != nil || same.Message.ID != first.Message.ID || same.Receipt != collab.ReceiptNotSent || same.Message.State != store.RoomMessageNotSent {
				t.Fatalf("same-key observation = %+v, %v; want original not_sent", same, err)
			}
			if prompts := rt.all()[0].wire.prompts(t); len(prompts) != 0 {
				t.Fatalf("same-key retry produced %d ACP prompts", len(prompts))
			}
			input.IdempotencyKey = "fresh-image"
			retry, err := room.Post(t.Context(), input)
			if err != nil || retry.Message.ID == first.Message.ID {
				t.Fatalf("fresh-key delivery = %+v, %v", retry, err)
			}
			if delayed {
				deliverDue()
			} else if retry.Receipt != collab.ReceiptSent || retry.Outcome != acphost.OutcomeSent {
				t.Fatalf("fresh-key receipt = %+v, want sent", retry)
			}
			sent, err := e.db.GetRoomMessage(t.Context(), retry.Message.ID)
			if err != nil || sent.State != store.RoomMessageSent || sent.DeliveredAt == nil || sent.Failure != nil || sent.AgentDelivery != "" {
				t.Fatalf("stored retry = %+v, %v; want sent without agent queueing", sent, err)
			}
			prompts := rt.all()[0].wire.prompts(t)
			if len(prompts) != 1 || len(prompts[0]) != 1 || prompts[0][0].Image == nil {
				t.Fatalf("fresh-key retry should produce exactly one image prompt: %+v", prompts)
			}
			img := prompts[0][0].Image
			decoded, decodeErr := base64.StdEncoding.DecodeString(img.Data)
			if decodeErr != nil || !bytes.Equal(decoded, data) || img.MimeType != "image/png" || img.Uri == nil || *img.Uri != "aether://room/"+retry.Message.ID+"/0" {
				t.Fatalf("retried image lost original bytes or message identity: %+v, %v", img, decodeErr)
			}
		})
	}
}

func TestACPAgentFailureIsNotMarkedPreDelivery(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run := e.launchACP(t, "")
	_, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptRefuse}, false, nil)
	if err == nil || errors.Is(err, domain.ErrPromptNotSent) || collab.ClassifyReceipt(err) != collab.ReceiptUncertain {
		t.Fatalf("agent refusal changed to a pre-delivery failure: %v", err)
	}
	if prompts := rt.all()[0].wire.prompts(t); len(prompts) != 1 {
		t.Fatalf("agent refusal must come from an actual wire prompt, got %d", len(prompts))
	}
}
