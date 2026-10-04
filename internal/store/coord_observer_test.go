package store

import (
	"context"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestUnackedMessageIDsRespectRecipientLimitAndAcknowledgement(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	from := mustCreateRun(t, db, workspace.ID, member.ID, domain.RunRunning)
	to := mustCreateRun(t, db, workspace.ID, member.ID, domain.RunRunning)
	for _, recipient := range []domain.RunID{to.ID, to.ID, from.ID} {
		if err := db.AppendRunMessage(ctx, &RunMessage{WorkspaceID: workspace.ID, FromRun: from.ID, ToRun: recipient, Body: "mail"}, 100); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := db.ListUnackedRunMessageIDs(ctx, to.ID, 1)
	if err != nil || len(ids) != 1 {
		t.Fatalf("bounded IDs = %v, %v", ids, err)
	}
	first, err := db.GetRunMessage(ctx, ids[0])
	if err != nil || first.ToRun != to.ID || first.DeliveryToken != "" || first.DeliveredAt != nil || first.AckedAt != nil {
		t.Fatalf("observation mutated or crossed recipient: %+v, %v", first, err)
	}
	batch, token, _, err := db.DeliverRunMessages(ctx, to.ID, "", 1)
	if err != nil || len(batch) != 1 || batch[0].ID != ids[0] || token == "" {
		t.Fatalf("delivery = %+v, %q, %v", batch, token, err)
	}
	stillUnread, err := db.ListUnackedRunMessageIDs(ctx, to.ID, 100)
	if err != nil || len(stillUnread) != 2 || stillUnread[0] != ids[0] {
		t.Fatalf("unacked delivered batch disappeared: %v, %v", stillUnread, err)
	}
	if _, _, _, ackErr := db.DeliverRunMessages(ctx, to.ID, token, 1); ackErr != nil {
		t.Fatal(ackErr)
	}
	afterAck, err := db.ListUnackedRunMessageIDs(ctx, to.ID, 100)
	if err != nil || len(afterAck) != 1 || afterAck[0] == ids[0] || afterAck[0] != stillUnread[1] {
		t.Fatalf("acknowledgement boundary = %v, %v", afterAck, err)
	}
	if _, err := db.ListUnackedRunMessageIDs(ctx, to.ID, 0); err == nil {
		t.Fatal("unbounded observer limit accepted")
	}
}
